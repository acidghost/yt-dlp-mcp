package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	buildVersion string
	buildCommit  string
	buildDate    string
)

type cli struct {
	Host           string        `default:"0.0.0.0" env:"YT_DLP_MCP_HOST" help:"HTTP listen host."`
	Port           int           `default:"3000" env:"YT_DLP_MCP_PORT" help:"HTTP listen port."`
	DefaultTimeout time.Duration `default:"60s" env:"YT_DLP_MCP_DEFAULT_TIMEOUT" help:"Default yt-dlp command timeout."`
	MaxTimeout     time.Duration `default:"5m" env:"YT_DLP_MCP_MAX_TIMEOUT" help:"Maximum yt-dlp command timeout."`
	StderrLimit    int64         `default:"65536" env:"YT_DLP_MCP_STDERR_LIMIT" help:"Maximum captured stderr bytes."`
	MaxConcurrency int           `default:"2" env:"YT_DLP_MCP_MAX_CONCURRENCY" help:"Maximum concurrent yt-dlp commands."`
	Version        bool          `help:"Print version and exit."`
}

type SearchVideosInput struct {
	Query     string `json:"query" jsonschema:"required"`
	Limit     int    `json:"limit,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type SearchVideosResult struct {
	Results []VideoResult `json:"results"`
}

type VideoResult struct {
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	ID         string   `json:"id,omitempty"`
	Duration   *float64 `json:"duration,omitempty"`
	Uploader   string   `json:"uploader,omitempty"`
	UploadDate string   `json:"upload_date,omitempty"`
}

type DownloadSubtitlesInput struct {
	URL       string `json:"url" jsonschema:"required"`
	Lang      string `json:"lang,omitempty"`
	Format    string `json:"format,omitempty" jsonschema:"Output format: text (default) returns merged plain text; timestamped_text returns compact timestamped lines for summarization and table-of-contents generation."`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type DownloadSubtitlesResult struct {
	Text   string `json:"text"`
	Lang   string `json:"lang"`
	Format string `json:"format"`
}

type ytdlp struct {
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	stderrLimit    int64
	sem            chan struct{}
	env            []string
}

type commandResult struct {
	Stdout     string
	Stderr     string
	DurationMS int64
}

type ytDLPVideo struct {
	Title       string   `json:"title"`
	WebpageURL  string   `json:"webpage_url"`
	OriginalURL string   `json:"original_url"`
	URL         string   `json:"url"`
	ID          string   `json:"id"`
	Duration    *float64 `json:"duration"`
	Uploader    string   `json:"uploader"`
	Channel     string   `json:"channel"`
	UploadDate  string   `json:"upload_date"`
}

type json3File struct {
	Events []json3Event `json:"events"`
}

type json3Event struct {
	TStartMS int64          `json:"tStartMs"`
	Segs     []json3Segment `json:"segs"`
}

type json3Segment struct {
	UTF8 string `json:"utf8"`
}

var (
	errBusy  = errors.New("concurrency limit reached")
	errLang  = errors.New("lang must be 1-32 chars containing only letters, numbers, dot, underscore, or dash")
	langRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	vttTagRe = regexp.MustCompile(`<[^>]*>`)
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
	maxQueryLength     = 300

	subtitleFormatText            = "text"
	subtitleFormatTimestampedText = "timestamped_text"
)

func main() {
	zerolog.TimeFieldFormat = time.RFC3339

	var c cli
	kong.Parse(&c, kong.Name("yt-dlp-mcp"), kong.Description("MCP server for YouTube search and subtitles via yt-dlp."))

	if c.Version {
		fmt.Printf("Version: %s\nCommit:  %s\nDate:    %s\n", buildVersion, buildCommit, buildDate)
		return
	}

	if err := c.validate(); err != nil {
		log.Fatal().Err(err).Msg("invalid configuration")
	}

	y := &ytdlp{
		defaultTimeout: c.DefaultTimeout,
		maxTimeout:     c.MaxTimeout,
		stderrLimit:    c.StderrLimit,
		sem:            make(chan struct{}, c.MaxConcurrency),
		env:            commandEnv(),
	}

	mcpSrv := mcpserver.NewMCPServer("yt-dlp-mcp", buildVersion)
	registerTools(mcpSrv, y)

	mcpHTTP := mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithStateLess(true))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/mcp", mcpHTTP)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", c.Host, c.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", httpSrv.Addr).Msg("starting server")
		errCh <- httpSrv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info().Str("signal", sig.String()).Msg("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("graceful shutdown failed")
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("server failed")
		}
	}
}

// validate checks that CLI configuration values are within acceptable ranges.
func (c cli) validate() error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("port out of range: %d", c.Port)
	}
	if c.DefaultTimeout <= 0 || c.MaxTimeout <= 0 || c.DefaultTimeout > c.MaxTimeout {
		return errors.New("timeouts must be positive and default-timeout must be <= max-timeout")
	}
	if c.StderrLimit <= 0 {
		return errors.New("stderr-limit must be positive")
	}
	if c.MaxConcurrency <= 0 {
		return errors.New("max-concurrency must be positive")
	}
	return nil
}

// registerTools registers the MCP tools (search_videos, download_subtitles)
// on the given MCP server, delegating execution to the provided ytdlp instance.
func registerTools(s *mcpserver.MCPServer, y *ytdlp) {
	s.AddTool(mcp.NewTool("search_videos",
		mcp.WithDescription("Search YouTube videos with yt-dlp."),
		mcp.WithInputSchema[SearchVideosInput](),
		mcp.WithOutputSchema[SearchVideosResult](),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in SearchVideosInput
		if err := req.BindArguments(&in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := y.searchVideos(ctx, in)
		return resultOrToolError(res, err)
	})

	s.AddTool(mcp.NewTool("download_subtitles",
		mcp.WithDescription("Download YouTube auto subtitles with yt-dlp and return merged text or compact timestamped text."),
		mcp.WithInputSchema[DownloadSubtitlesInput](),
		mcp.WithOutputSchema[DownloadSubtitlesResult](),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in DownloadSubtitlesInput
		if err := req.BindArguments(&in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := y.downloadSubtitles(ctx, in)
		return subtitleResultOrToolError(res, err)
	})
}

// resultOrToolError returns a structured MCP tool result on success, or an
// MCP tool-error result when err is non-nil.
func resultOrToolError[T any](res T, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructuredOnly(res), nil
}

// subtitleResultOrToolError returns full subtitle data only in structuredContent.
// The compact content fallback avoids doubling large subtitle responses.
func subtitleResultOrToolError(res DownloadSubtitlesResult, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	fallback := fmt.Sprintf("Downloaded %s subtitles for %s (%d bytes).", res.Format, res.Lang, len(res.Text))
	return mcp.NewToolResultStructured(res, fallback), nil
}

// searchVideos searches YouTube for videos matching the given query using
// yt-dlp and returns up to the requested limit of results.
func (y *ytdlp) searchVideos(ctx context.Context, in SearchVideosInput) (SearchVideosResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return SearchVideosResult{}, errors.New("query is required")
	}
	if len(query) > maxQueryLength {
		return SearchVideosResult{}, fmt.Errorf("query exceeds max length of %d", maxQueryLength)
	}

	limit := in.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if limit < 1 || limit > maxSearchLimit {
		return SearchVideosResult{}, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}

	res, err := y.run(ctx, []string{"--skip-download", "--dump-json", fmt.Sprintf("ytsearch%d:%s", limit, query)}, "", in.TimeoutMS)
	if err != nil {
		return SearchVideosResult{}, err
	}

	videos, err := parseSearchResults(res.Stdout)
	if err != nil {
		return SearchVideosResult{}, err
	}
	return SearchVideosResult{Results: videos}, nil
}

// downloadSubtitles downloads auto-generated subtitles for the given video URL
// in the requested language and returns the requested output format.
func (y *ytdlp) downloadSubtitles(ctx context.Context, in DownloadSubtitlesInput) (DownloadSubtitlesResult, error) {
	rawURL := strings.TrimSpace(in.URL)
	if err := validateHTTPURL(rawURL); err != nil {
		return DownloadSubtitlesResult{}, err
	}

	lang := strings.TrimSpace(in.Lang)
	if lang == "" {
		lang = "en"
	}
	if !langRe.MatchString(lang) {
		return DownloadSubtitlesResult{}, errLang
	}

	format := strings.TrimSpace(strings.ToLower(in.Format))
	if format == "" {
		format = subtitleFormatText
	}
	if format != subtitleFormatText && format != subtitleFormatTimestampedText {
		return DownloadSubtitlesResult{}, fmt.Errorf("format must be %q or %q", subtitleFormatText, subtitleFormatTimestampedText)
	}

	dir, err := os.MkdirTemp("", "yt-dlp-mcp-*")
	if err != nil {
		return DownloadSubtitlesResult{}, err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Warn().
				Err(err).
				Str("dir", dir).
				Msg("failed to remove temp dir")
		}
	}()

	args := []string{
		"--skip-download",
		"--no-playlist",
		"--write-auto-subs",
		"--sub-langs", lang,
		"--sub-format", "json3/vtt",
		"--output", "%(id)s.%(ext)s",
		rawURL,
	}
	if _, err := y.run(ctx, args, dir, in.TimeoutMS); err != nil {
		return DownloadSubtitlesResult{}, err
	}

	path, err := findFileByExt(dir, ".json3")
	if err != nil {
		// Fall back to VTT subtitles.
		path, err = findFileByExt(dir, ".vtt")
		if err != nil {
			return DownloadSubtitlesResult{}, err
		}
	}

	var text string
	if format == subtitleFormatTimestampedText && strings.HasSuffix(path, ".json3") {
		text, err = extractJSON3TimestampedText(path)
	} else if format == subtitleFormatTimestampedText {
		text, err = extractVTTTimestampedText(path)
	} else if strings.HasSuffix(path, ".json3") {
		text, err = extractJSON3Text(path)
	} else {
		text, err = extractVTTText(path)
	}
	if err != nil {
		return DownloadSubtitlesResult{}, err
	}
	if text == "" {
		return DownloadSubtitlesResult{}, errors.New("subtitle file contained no text")
	}

	return DownloadSubtitlesResult{Text: text, Lang: lang, Format: format}, nil
}

// run executes yt-dlp with the given arguments inside an optional working
// directory, enforcing a timeout and the concurrency semaphore.
func (y *ytdlp) run(ctx context.Context, args []string, dir string, timeoutMS int64) (commandResult, error) {
	timeout := y.defaultTimeout
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if timeout <= 0 || timeout > y.maxTimeout {
		return commandResult{}, fmt.Errorf("timeout_ms must be > 0 and <= %d", y.maxTimeout.Milliseconds())
	}

	select {
	case y.sem <- struct{}{}:
		defer func() { <-y.sem }()
	default:
		return commandResult{}, errBusy
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // Arguments are intentionally built from tool input and passed without shell expansion.
	cmd := exec.Command("yt-dlp", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = y.env
	cmd.Stdin = strings.NewReader("")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout bytes.Buffer
	stderr := &limitedBuffer{limit: y.stderrLimit}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	log.Info().Strs("yt-dlp", args).Msg("running command")

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return commandResult{}, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return commandResult{}, fmt.Errorf("yt-dlp timed out after %s", timeout)
	}

	result := commandResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: time.Since(started).Milliseconds(),
	}

	log.Info().
		Str("tool_event", "yt_dlp_complete").
		Int64("duration_ms", result.DurationMS).
		Bool("stderr_truncated", stderr.truncated).
		Msg("yt-dlp completed")

	if waitErr != nil {
		stderr := strings.TrimSpace(result.Stderr)
		if stderr != "" {
			stderr = ": " + stderr
		}
		return commandResult{}, fmt.Errorf("yt-dlp failed: %w%s", waitErr, stderr)
	}
	return result, nil
}

// parseSearchResults parses yt-dlp's line-delimited JSON output into a
// slice of VideoResult values.
func parseSearchResults(stdout string) ([]VideoResult, error) {
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	videos := make([]VideoResult, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw ytDLPVideo
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("parse yt-dlp JSON: %w", err)
		}

		videoURL := raw.WebpageURL
		if videoURL == "" {
			videoURL = raw.OriginalURL
		}
		if videoURL == "" && strings.HasPrefix(raw.URL, "http") {
			videoURL = raw.URL
		}

		uploader := raw.Uploader
		if uploader == "" {
			uploader = raw.Channel
		}

		videos = append(videos, VideoResult{
			Title:      raw.Title,
			URL:        videoURL,
			ID:         raw.ID,
			Duration:   raw.Duration,
			Uploader:   uploader,
			UploadDate: raw.UploadDate,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return videos, nil
}

// validateHTTPURL checks that rawURL is a non-empty HTTP or HTTPS URL
// with a host component.
func validateHTTPURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url must start with http:// or https://")
	}
	if parsed.Host == "" {
		return errors.New("url host is required")
	}
	return nil
}

// findFileByExt walks dir looking for a file with the given extension
// and returns the first match.
func findFileByExt(dir, ext string) (string, error) {
	var matches []string
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ext {
			return nil
		}
		matches = append(matches, path)
		return nil
	}); err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no %s subtitles found for requested language", ext)
	}
	return matches[0], nil
}

// extractJSON3Text reads a YouTube json3 subtitle file and returns the
// concatenated, whitespace-normalised text.
func extractJSON3Text(path string) (string, error) {
	//nolint:gosec // path is selected from a per-request temp directory created by this process.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var sub json3File
	if err := json.Unmarshal(data, &sub); err != nil {
		return "", err
	}

	parts := make([]string, 0)
	for _, event := range sub.Events {
		text := json3EventText(event)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " "), nil
}

// extractJSON3TimestampedText reads a YouTube json3 subtitle file and returns
// compact timestamped subtitle lines.
func extractJSON3TimestampedText(path string) (string, error) {
	//nolint:gosec // path is selected from a per-request temp directory created by this process.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var sub json3File
	if err := json.Unmarshal(data, &sub); err != nil {
		return "", err
	}

	lines := make([]string, 0)
	var previous string
	for _, event := range sub.Events {
		text := json3EventText(event)
		if text == "" || text == previous {
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", formatTimestamp(event.TStartMS), text))
		previous = text
	}
	return strings.Join(lines, "\n"), nil
}

func json3EventText(event json3Event) string {
	parts := make([]string, 0, len(event.Segs))
	for _, seg := range event.Segs {
		parts = append(parts, seg.UTF8)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// extractVTTText reads a WebVTT subtitle file and returns the concatenated,
// whitespace-normalised text with VTT tags stripped.
func extractVTTText(path string) (string, error) {
	//nolint:gosec // path is selected from a per-request temp directory created by this process.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var parts []string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		// Skip blank lines, header lines, and timestamp lines.
		if trimmed == "" ||
			strings.HasPrefix(trimmed, "WEBVTT") ||
			strings.HasPrefix(trimmed, "Kind:") ||
			strings.HasPrefix(trimmed, "Language:") ||
			strings.Contains(trimmed, "-->") {
			continue
		}
		// Strip VTT formatting tags (e.g. <b>, </b>, <c.colorCCCCCC>).
		trimmed = strings.TrimSpace(vttTagRe.ReplaceAllString(trimmed, ""))
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " "), nil
}

// extractVTTTimestampedText reads a WebVTT subtitle file and returns compact
// timestamped subtitle lines.
func extractVTTTimestampedText(path string) (string, error) {
	//nolint:gosec // path is selected from a per-request temp directory created by this process.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	var cueStart string
	var cueParts []string
	var previous string

	flushCue := func() {
		if cueStart == "" {
			cueParts = nil
			return
		}
		text := normalizeVTTText(cueParts)
		if text != "" && text != previous {
			lines = append(lines, fmt.Sprintf("[%s] %s", cueStart, text))
			previous = text
		}
		cueStart = ""
		cueParts = nil
	}

	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		switch {
		case trimmed == "":
			flushCue()
		case strings.HasPrefix(trimmed, "WEBVTT") ||
			strings.HasPrefix(trimmed, "Kind:") ||
			strings.HasPrefix(trimmed, "Language:"):
			continue
		case strings.Contains(trimmed, "-->"):
			flushCue()
			start, _, _ := strings.Cut(trimmed, "-->")
			cueStart = formatVTTTimestamp(strings.TrimSpace(start))
		default:
			cueParts = append(cueParts, trimmed)
		}
	}
	flushCue()
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func normalizeVTTText(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(vttTagRe.ReplaceAllString(part, ""))
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(strings.Fields(strings.Join(cleaned, " ")), " ")
}

func formatTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	totalSeconds := ms / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func formatVTTTimestamp(ts string) string {
	ms, ok := parseVTTTimestamp(ts)
	if !ok {
		return ts
	}
	return formatTimestamp(ms)
}

func parseVTTTimestamp(ts string) (int64, bool) {
	parts := strings.Split(ts, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}

	multiplier := int64(60)
	totalSeconds := int64(0)
	for i := len(parts) - 2; i >= 0; i-- {
		value, err := strconv.ParseInt(parts[i], 10, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		totalSeconds += value * multiplier
		multiplier *= 60
	}

	secondsPart := strings.ReplaceAll(parts[len(parts)-1], ",", ".")
	secondsPieces := strings.SplitN(secondsPart, ".", 2)
	seconds, err := strconv.ParseInt(secondsPieces[0], 10, 64)
	if err != nil || seconds < 0 {
		return 0, false
	}

	milliseconds := int64(0)
	if len(secondsPieces) == 2 {
		fraction := secondsPieces[1]
		if len(fraction) > 3 {
			fraction = fraction[:3]
		}
		for len(fraction) < 3 {
			fraction += "0"
		}
		milliseconds, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, false
		}
	}

	return (totalSeconds+seconds)*1000 + milliseconds, true
}

// commandEnv builds a minimal environment variable slice containing only
// the allowlisted keys needed by yt-dlp, falling back to a default PATH.
func commandEnv() []string {
	allowed := []string{"PATH", "HOME", "TERM", "LANG", "LC_ALL", "XDG_CACHE_HOME", "DENO_DIR"}
	env := make([]string, 0, len(allowed))
	seenPath := false
	for _, key := range allowed {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
			if key == "PATH" {
				seenPath = true
			}
		}
	}
	if !seenPath {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}

type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

// Write appends p to the buffer, discarding bytes once the size limit is
// reached and setting the truncated flag.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

// String returns the buffered content in a concurrency-safe manner.
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*limitedBuffer)(nil)

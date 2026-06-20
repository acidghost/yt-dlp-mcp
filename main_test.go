package main

import (
	"os"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestExtractJSON3TimestampedText(t *testing.T) {
	path := writeTempFile(t, `{
		"events": [
			{"tStartMs": 12345, "segs": [{"utf8": "Hello"}, {"utf8": " world"}]},
			{"tStartMs": 13000, "segs": [{"utf8": "Hello world"}]},
			{"tStartMs": 62100, "segs": [{"utf8": "Next"}, {"utf8": "\n"}, {"utf8": "chapter"}]}
		]
	}`)

	got, err := extractJSON3TimestampedText(path)
	if err != nil {
		t.Fatal(err)
	}

	want := "[00:00:12] Hello world\n[00:01:02] Next chapter"
	if got != want {
		t.Fatalf("timestamped json3 text mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestExtractVTTTimestampedText(t *testing.T) {
	path := writeTempFile(t, `WEBVTT
Kind: captions
Language: en

00:00:01.250 --> 00:00:03.000
<c>Hello</c> <00:00:02.000><c>world</c>

00:01:02.500 --> 00:01:05.000
Next
chapter
`)

	got, err := extractVTTTimestampedText(path)
	if err != nil {
		t.Fatal(err)
	}

	want := "[00:00:01] Hello world\n[00:01:02] Next chapter"
	if got != want {
		t.Fatalf("timestamped vtt text mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestSubtitleResultDoesNotDuplicatePayloadInFallback(t *testing.T) {
	payload := strings.Repeat("WEBVTT\n", 100)
	result, err := subtitleResultOrToolError(DownloadSubtitlesResult{
		Text:   payload,
		Lang:   "it",
		Format: subtitleFormatTimestampedText,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.StructuredContent == nil {
		t.Fatal("structured content was not set")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one fallback content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("fallback content has type %T, want mcp.TextContent", result.Content[0])
	}
	if strings.Contains(text.Text, "WEBVTT") {
		t.Fatalf("fallback content duplicated subtitle payload: %q", text.Text)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "subtitle-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

package subs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const srtWithBOM = "\ufeff1\n00:00:01,000 --> 00:00:03,500\nHello there.\n\n2\n00:00:04,000 --> 00:00:06,000\n<i>General</i> Kenobi!\nYou are a bold one.\n\n3\n00:00:05,500 --> 00:00:07,000\nOverlapping cue.\n"

const vttSample = "WEBVTT\n\n00:01.000 --> 00:03.000\nFirst line.\n\n00:04.000 --> 00:06.500\n<b>Bold</b> words here.\n"

func writeTmp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseSRT(t *testing.T) {
	cues, err := ParseFile(writeTmp(t, "sample.srt", srtWithBOM))
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 3 {
		t.Fatalf("got %d cues, want 3", len(cues))
	}
	if cues[0].Start != 1.0 || cues[0].End != 3.5 {
		t.Errorf("cue0 timing = %v-%v", cues[0].Start, cues[0].End)
	}
	if cues[0].Text != "Hello there." {
		t.Errorf("cue0 text = %q", cues[0].Text)
	}
	if cues[1].Text != "General Kenobi! You are a bold one." {
		t.Errorf("cue1 text = %q (tags/lines should be normalized)", cues[1].Text)
	}
	if cues[2].Start >= cues[1].End {
		t.Errorf("overlapping cue order broken")
	}
}

func TestParseVTT(t *testing.T) {
	cues, err := ParseFile(writeTmp(t, "sample.vtt", vttSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2", len(cues))
	}
	if cues[0].Start != 1.0 || cues[0].End != 3.0 {
		t.Errorf("cue0 timing = %v-%v", cues[0].Start, cues[0].End)
	}
	if cues[1].Text != "Bold words here." {
		t.Errorf("cue1 text = %q", cues[1].Text)
	}
}

func TestPickSidecar(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Movie (2019).mkv")
	files := map[string]int{
		"Movie (2019).srt":     1,
		"Movie (2019).en.srt":  2,
		"Movie (2019).fr.srt":  0,
		"Other (2019).srt":     0,
		"Movie (2019).eng.vtt": 2,
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(srtWithBOM), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := PickSidecar(video)
	want := "Movie (2019).en.srt"
	if !strings.HasSuffix(got, want) {
		t.Errorf("PickSidecar = %q, want suffix %q", got, want)
	}
	if PickSidecar(filepath.Join(dir, "Nothing.mkv")) != "" {
		t.Errorf("expected empty pick for unmatched video")
	}
}

func TestPickSidecarForced(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Show.mkv")
	for _, n := range []string{"Show.en.srt", "Show.sdh.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("1\n00:00:00,000 --> 00:00:01,000\nx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := PickSidecar(video); !strings.HasSuffix(got, "Show.en.srt") {
		t.Errorf("plain en sidecar must beat SDH, got %q", got)
	}
}

func TestPickSidecarForcedOnly(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Show.mkv")
	if err := os.WriteFile(filepath.Join(dir, "Show.forced.en.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PickSidecar(video); got == "" {
		t.Errorf("forced-only en sidecar should still be selected as last resort")
	}
}

func TestEnglishStream(t *testing.T) {
	if got := EnglishStream([]string{"und", "eng", "fre"}); got != 1 {
		t.Errorf("EnglishStream = %d, want 1", got)
	}
	if got := EnglishStream([]string{"und"}); got != -1 {
		t.Errorf("single non-english stream should not match, got %d", got)
	}
	if got := EnglishStream([]string{""}); got != 0 {
		t.Errorf("single untagged stream should match, got %d", got)
	}
	if got := EnglishStream([]string{"", "eng"}); got != 1 {
		t.Errorf("explicit tag should beat untagged, got %d", got)
	}
}

func TestParseBytesSRT(t *testing.T) {
	cues, err := ParseBytes([]byte(srtWithBOM), ".srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 3 {
		t.Fatalf("got %d cues, want 3", len(cues))
	}
	if cues[1].Text != "General Kenobi! You are a bold one." {
		t.Errorf("cue1 text = %q", cues[1].Text)
	}
	if cues[0].Start != 1.0 || cues[2].End != 7.0 {
		t.Errorf("timings = %v-%v, %v-%v", cues[0].Start, cues[0].End, cues[2].Start, cues[2].End)
	}
}

func TestParseBytesVTT(t *testing.T) {
	cues, err := ParseBytes([]byte(vttSample), ".vtt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2", len(cues))
	}
	if cues[1].Text != "Bold words here." || cues[1].Start != 4.0 || cues[1].End != 6.5 {
		t.Errorf("cue1 = %+v", cues[1])
	}
}

func TestParseBytesVTTCRLF(t *testing.T) {
	data := "WEBVTT\r\n\r\n00:01.000 --> 00:03.000\r\nFirst line.\r\n\r\n00:04.000 --> 00:05.000\r\nSecond.\r\n"
	cues, err := ParseBytes([]byte(data), ".VTT")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 || cues[0].Text != "First line." || cues[1].Text != "Second." {
		t.Errorf("CRLF VTT cues = %+v", cues)
	}
}

func TestParseBytesError(t *testing.T) {
	if _, err := ParseBytes([]byte("1\nbroken --> stamp\n"), ".srt"); err == nil {
		t.Errorf("expected error for malformed srt bytes")
	}
}

func TestParseFileMissing(t *testing.T) {
	if _, err := ParseFile(filepath.Join(t.TempDir(), "absent.srt")); err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestParseFileMalformed(t *testing.T) {
	if _, err := ParseFile(writeTmp(t, "bad.srt", "1\n00:00:00,000 --> notatime\nx\n")); err == nil {
		t.Errorf("expected error for malformed srt content")
	}
}

func TestParseStrayTags(t *testing.T) {
	// A stray '>' passes through; an unclosed '<' swallows the rest of the line.
	cues, err := ParseBytes([]byte("1\n00:00:01,000 --> 00:00:02,000\na > b <unclosed tail\n"), ".srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 1 || cues[0].Text != "a > b" {
		t.Errorf("stray-tag text = %+v", cues)
	}
}

func TestOffset(t *testing.T) {
	cues := []Cue{
		{Start: 1, End: 2, Text: "a"},
		{Start: -1, End: 0.5, Text: "b"},
	}
	out := Offset(cues, 0.75)
	if out[0].Start != 1.75 || out[0].End != 2.75 {
		t.Errorf("cue0 = %+v", out[0])
	}
	if out[1].Start != 0 || out[1].End != 1.25 {
		t.Errorf("negative start must clamp to 0, got %+v", out[1])
	}
	if len(cues) != 2 || cues[0].Start != 1.75 {
		t.Errorf("Offset should shift in place")
	}
}

func TestPickSidecarSkipsDirsAndOtherExts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Show.srt"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Show.nfo", "Show.en.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("1\n00:00:00,000 --> 00:00:01,000\nx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := PickSidecar(filepath.Join(dir, "Show.mkv"))
	if !strings.HasSuffix(got, "Show.en.srt") {
		t.Errorf("expected file Show.en.srt to win over directory/other exts, got %q", got)
	}
}

func TestStripTags(t *testing.T) {
	if got := stripTags("<i>bold</i> ok"); got != "bold ok" {
		t.Errorf("stripTags = %q, want %q", got, "bold ok")
	}
	if got := stripTags("a > b <tail"); got != "a > b " {
		t.Errorf("stray > must pass through and unterminated < must swallow, got %q", got)
	}
}

func TestFromAstisubSkipsBadItems(t *testing.T) {
	// Reversed timestamps and whitespace-only text must not produce cues.
	cues, err := ParseBytes([]byte("1\n00:00:03,000 --> 00:00:01,000\nreversed\n\n2\n00:00:05,000 --> 00:00:06,000\n   \n"), ".srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 0 {
		t.Errorf("want 0 cues, got %+v", cues)
	}
}

func TestPickSidecarMissingDir(t *testing.T) {
	if got := PickSidecar(filepath.Join(t.TempDir(), "nope", "X.mkv")); got != "" {
		t.Errorf("expected empty pick for unreadable dir, got %q", got)
	}
}

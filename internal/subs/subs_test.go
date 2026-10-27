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
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
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
		if err := os.WriteFile(filepath.Join(dir, name), []byte(srtWithBOM), 0o644); err != nil {
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
		if err := os.WriteFile(filepath.Join(dir, n), []byte("1\n00:00:00,000 --> 00:00:01,000\nx\n"), 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "Show.forced.en.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\nx\n"), 0o644); err != nil {
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

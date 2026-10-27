package chunks

import "testing"

func mkCues(n int, text string, gap float64) []Cue {
	var out []Cue
	t := 0.0
	for i := 0; i < n; i++ {
		out = append(out, Cue{Idx: i, StartSec: t, EndSec: t + 1.5, Text: text})
		t += 1.5 + gap
	}
	return out
}

func TestBuildMergeLimit(t *testing.T) {
	cues := mkCues(10, "short line", 0)
	out := Build(cues)
	if len(out) != 4 { // 3 cues per chunk max
		t.Fatalf("got %d chunks, want 4", len(out))
	}
	for i, ch := range out {
		if ch.StartCueIdx != i*3 {
			t.Errorf("chunk %d start = %d", i, ch.StartCueIdx)
		}
		last := ch.EndCueIdx
		if last > (i+1)*3-1 {
			t.Errorf("chunk %d end = %d beyond cue count", i, last)
		}
	}
}

func TestBuildCharLimit(t *testing.T) {
	long := "this cue text is forty characters exactly!."
	// 4 cues of ~43 chars each: first chunk fits 3 cues max anyway
	cues := mkCues(4, long, 0)
	out := Build(cues)
	// cue0+cue1+cue2 = 43*3+2 spaces = 131 chars, cue3 pushes over only via cue count (4>3)
	if len(out) != 2 || out[1].StartCueIdx != 3 {
		t.Fatalf("unexpected chunking: %+v", out)
	}
}

func TestBuildSingleOversizedCue(t *testing.T) {
	big := make([]byte, 400)
	for i := range big {
		big[i] = 'a'
	}
	cues := []Cue{{Idx: 0, StartSec: 0, EndSec: 2, Text: string(big)}}
	out := Build(cues)
	if len(out) != 1 || out[0].Text != string(big) {
		t.Fatalf("oversized cue must become its own chunk, got %+v", out)
	}
}

func TestBuildGapClose(t *testing.T) {
	cues := []Cue{
		{Idx: 0, StartSec: 0, EndSec: 2, Text: "before silence"},
		{Idx: 1, StartSec: 40, EndSec: 42, Text: "after silence"},
	}
	out := Build(cues)
	if len(out) != 2 {
		t.Fatalf("gap should close chunks, got %+v", out)
	}
}

func TestBuildEmpty(t *testing.T) {
	if out := Build(nil); len(out) != 0 {
		t.Fatalf("empty input produced %+v", out)
	}
	cues := []Cue{{Idx: 0, StartSec: 0, EndSec: 1, Text: "   "}}
	if out := Build(cues); len(out) != 0 {
		t.Fatalf("whitespace-only cue should be dropped, got %+v", out)
	}
}

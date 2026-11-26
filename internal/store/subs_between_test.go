package store

import (
	"path/filepath"
	"testing"
)

func TestCuesBetween(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close: %v", cerr)
		}
	}()

	id, _, err := st.UpsertMedia("/media/ep.mkv", "mkv", 100, 1, "Ep", 2024, 1, 1, "Show")
	if err != nil {
		t.Fatalf("UpsertMedia: %v", err)
	}
	trackID, err := st.ReplaceTrack(id, &SubtitleTrack{Source: "sidecar", Lang: "en"})
	if err != nil {
		t.Fatalf("ReplaceTrack: %v", err)
	}
	if rerr := st.ReplaceCues(id, trackID, []Cue{
		{Idx: 0, StartSec: 5.0, EndSec: 10.0, Text: "before touching start"},
		{Idx: 1, StartSec: 10.0, EndSec: 12.0, Text: "first selected"},
		{Idx: 2, StartSec: 12.2, EndSec: 15.0, Text: "second selected"},
		{Idx: 3, StartSec: 15.0, EndSec: 18.0, Text: "after touching end"},
	}); rerr != nil {
		t.Fatalf("ReplaceCues: %v", rerr)
	}

	got, err := st.CuesBetween(id, 10.0, 15.0)
	if err != nil {
		t.Fatalf("CuesBetween: %v", err)
	}
	if len(got) != 2 || got[0].Idx != 1 || got[1].Idx != 2 {
		t.Fatalf("got cues = %+v, want idx 1 and 2", got)
	}

	// Very short span still matches overlapping cue without collapsing bounds.
	tiny, err := st.CuesBetween(id, 10.5, 10.51)
	if err != nil {
		t.Fatalf("CuesBetween tiny: %v", err)
	}
	if len(tiny) != 1 || tiny[0].Idx != 1 {
		t.Fatalf("tiny cues = %+v, want idx 1", tiny)
	}
}

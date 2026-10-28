package store

import (
	"reflect"
	"testing"
)

func TestPopularExamples(t *testing.T) {
	s := openTest(t)

	media := func(path, status string) int64 {
		t.Helper()
		id, _, err := s.UpsertMedia(path, "mkv", 100, 1000, "T", 2020, 0, 0, "")
		if err != nil {
			t.Fatalf("UpsertMedia: %v", err)
		}
		if err := s.SetMediaStatus(id, status, ""); err != nil {
			t.Fatalf("SetMediaStatus: %v", err)
		}
		return id
	}
	// put swaps in a single chunk for a media file and optionally stamps it as
	// indexed, i.e. present in the search index.
	put := func(mediaID int64, text string, indexed bool) {
		t.Helper()
		if err := s.ReplaceChunks(mediaID, []Chunk{{StartCueIdx: 0, EndCueIdx: 1, Text: text}}); err != nil {
			t.Fatalf("ReplaceChunks: %v", err)
		}
		if !indexed {
			return
		}
		chunks, err := s.ChunksForMedia(mediaID)
		if err != nil {
			t.Fatalf("ChunksForMedia: %v", err)
		}
		ids := make([]int64, 0, len(chunks))
		for _, ch := range chunks {
			ids = append(ids, ch.ID)
		}
		if err := s.MarkChunksIndexed(ids, 1); err != nil {
			t.Fatalf("MarkChunksIndexed: %v", err)
		}
	}

	put(media("/a.mkv", "indexed"), "Show me the money!", true)
	put(media("/b.mkv", "indexed"), "show me the money", true)
	put(media("/c.mkv", "indexed"), "run, you fools", true)
	put(media("/d.mkv", "indexed"), "the city never sleeps", false) // not yet in the search index
	put(media("/e.mkv", "error"), "i could have been a contender", true)
	put(media("/f.mkv", "no_subs"), "run, you fools", true)
	put(media("/g.mkv", "indexed"), "   ", true)

	candidates := []string{
		"show me the money",
		"the city never sleeps",
		"i could have been a contender",
		"run, you fools",
		"hello world",
	}
	got, err := s.PopularExamples(candidates)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"show me the money", "run, you fools"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PopularExamples = %v, want %v", got, want)
	}

	if got, err := s.PopularExamples(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("PopularExamples(nil) = %v, %v; want empty non-nil", got, err)
	}
}

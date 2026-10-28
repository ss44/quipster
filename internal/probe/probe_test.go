package probe

import "testing"

func TestPickEnglishAudio(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "eac3", Lang: "en", Default: true},
		{Ordinal: 1, Codec: "ac3", Lang: "fr"},
		{Ordinal: 2, Codec: "aac", Lang: "en", Title: "Commentary"},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 0 {
		t.Fatalf("expected English track 0, got %+v", got)
	}
}

func TestPickEnglishAudioSkipsCommentary(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "eac3", Lang: "en", Title: "Director Commentary"},
		{Ordinal: 1, Codec: "ac3", Lang: "eng"},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 1 {
		t.Fatalf("expected non-commentary English track 1, got %+v", got)
	}
}

func TestPickEnglishAudioPrefersDefaultEnglish(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "aac", Lang: "en"},
		{Ordinal: 1, Codec: "eac3", Lang: "en", Default: true},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 1 {
		t.Fatalf("expected default English track 1, got %+v", got)
	}
}

func TestPickEnglishAudioFallsBackToDefaultTrack(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "aac", Lang: "ja"},
		{Ordinal: 1, Codec: "eac3", Lang: "fr", Default: true},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 1 {
		t.Fatalf("expected default fallback track 1, got %+v", got)
	}
}

func TestPickEnglishAudioFirstTrackWhenUntagged(t *testing.T) {
	tracks := []AudioStream{{Ordinal: 0, Codec: "aac", Lang: ""}}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 0 {
		t.Fatalf("expected sole untagged track 0, got %+v", got)
	}
}

func TestPickEnglishAudioEmpty(t *testing.T) {
	if got := PickEnglishAudio(nil); got != nil {
		t.Fatalf("expected nil for empty tracks, got %+v", got)
	}
}

func TestPickEnglishAudioAllUnusable(t *testing.T) {
	tracks := []AudioStream{
		{Ordinal: 0, Codec: "aac", Lang: "en", Title: "Commentary"},
		{Ordinal: 1, Codec: "ac3", Lang: "en", Title: "Audio Description"},
	}
	got := PickEnglishAudio(tracks)
	if got == nil || got.Ordinal != 0 {
		t.Fatalf("expected raw first track when every track is commentary/description, got %+v", got)
	}
}

package scan

import "testing"

func TestParseTV(t *testing.T) {
	cases := []struct {
		rel                   string
		show, title           string
		year, season, episode int
	}{
		{
			rel:     "tv/Link Click/Season 1/Link Click (2021) - S01E11 - Pinnacle of Light [WEBRip-1080p][EAC3 2.0][x265]-iVy.mkv",
			show:    "Link Click",
			title:   "Pinnacle of Light",
			year:    2021,
			season:  1,
			episode: 11,
		},
		{
			rel:     "TV Shows/Breaking Bad (2008)/Season 02/Breaking.Bad.S02E05.Madrigal.720p.mkv",
			show:    "Breaking Bad",
			title:   "Madrigal",
			year:    2008,
			season:  2,
			episode: 5,
		},
		{
			rel:     "tv-anime/Cowboy Bebop/Season 1/Cowboy Bebop 1x02 - Stray Dog Strut.mkv",
			show:    "Cowboy Bebop",
			title:   "Stray Dog Strut",
			season:  1,
			episode: 2,
		},
		{
			rel:     "tv/Severance/Season 2/03 - Who Is Alive.mkv",
			show:    "Severance",
			title:   "Who Is Alive",
			season:  2,
			episode: 3,
		},
	}
	for _, c := range cases {
		got := Parse(c.rel)
		if got.ShowTitle != c.show {
			t.Errorf("%s: show = %q, want %q", c.rel, got.ShowTitle, c.show)
		}
		if got.Title != c.title {
			t.Errorf("%s: title = %q, want %q", c.rel, got.Title, c.title)
		}
		if got.Year != c.year {
			t.Errorf("%s: year = %d, want %d", c.rel, got.Year, c.year)
		}
		if got.Season != c.season || got.Episode != c.episode {
			t.Errorf("%s: s%02de%02d, want s%02de%02d", c.rel, got.Season, got.Episode, c.season, c.episode)
		}
		if !got.IsTV {
			t.Errorf("%s: IsTV = false", c.rel)
		}
	}
}

func TestParseMovies(t *testing.T) {
	cases := []struct {
		rel, title string
		year       int
	}{
		{"movies/The Matrix (1999)/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", "The Matrix", 1999},
		{"movies/Inception (2010)/Inception (2010).mkv", "Inception", 2010},
		{"Movies/Arrival.2016.720p.WEB-DL.mkv", "Arrival", 2016},
		{"Movies/1983 - Something (2019)/1983 Something (2019).mkv", "1983 Something", 2019},
		{"movies/Stand By Me.mkv", "Stand By Me", 0},
		{"movies/1917 (2019)/1917 (2019).mkv", "1917", 2019},
		{"movies/2001 A Space Odyssey (1968)/2001 A Space Odyssey (1968).mkv", "2001 A Space Odyssey", 1968},
		{"movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv", "Blade Runner 2049", 2017},
		{"movies/2067 (2020)/2067 - (2020) - WEBDL-1080p.mkv", "2067", 2020},
		{"movies/846 (2020)/846 (2020) {imdb-tt12498618} [WEBRip-720p][AAC 2.0][x264]-LAMA.mp4", "846", 2020},
		{"movies/The Tank (2025)/The Tank (2025) {imdb-tt31495504} [WEBDL-2160p][EAC3 5.1][h265]-BiVerseADWeb.mkv", "The Tank", 2025},
		{"movies/It's a Wonderful Knife (2023)/It's a Wonderful Knife (2023) {imdb-tt27619382} [WEBDL-1080p][EAC3 5.1][h264]-FLUX.mkv", "It's a Wonderful Knife", 2023},
	}
	for _, c := range cases {
		got := Parse(c.rel)
		if got.IsTV {
			t.Errorf("%s: unexpectedly TV", c.rel)
		}
		if got.Title != c.title {
			t.Errorf("%s: title = %q, want %q", c.rel, got.Title, c.title)
		}
		if got.Year != c.year {
			t.Errorf("%s: year = %d, want %d", c.rel, got.Year, c.year)
		}
	}
}

func TestStripTrailingGroup(t *testing.T) {
	cases := map[string]string{
		"Pinnacle of Light -iVy":       "Pinnacle of Light",
		"Secret Recipe -ADWeb":         "Secret Recipe",
		"Search of the Child -playWEB": "Search of the Child",
		"Spider-Man":                   "Spider-Man",
		"Bill - The Movie":             "Bill - The Movie",
		"Fury - 2014":                  "Fury - 2014",
		"Trap -TRAILER":                "Trap",
	}
	for in, want := range cases {
		if got := stripTrailingGroup(in); got != want {
			t.Errorf("stripTrailingGroup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanTitleTags(t *testing.T) {
	if got := cleanTitle("Show.Name.2019.1080p.WEB-DL.DDP5.1.H.264-GRP"); got != "Show Name 2019" {
		t.Errorf("cleanTitle = %q", got)
	}
	if got := cleanTitle("Movie [rarbg] Name (2010)"); got != "Movie Name (2010)" {
		t.Errorf("cleanTitle = %q", got)
	}
	if got := cleanTitle("Link Click (2021) - Pinnacle of Light [WEBRip-1080p][EAC3 2.0][x265]-iVy"); got != "Link Click (2021) - Pinnacle of Light" {
		t.Errorf("cleanTitle = %q", got)
	}
}

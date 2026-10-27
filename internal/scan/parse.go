package scan

import (
	"regexp"
	"strconv"
	"strings"
)

type Parsed struct {
	Title     string
	ShowTitle string
	Year      int
	Season    int
	Episode   int
	IsTV      bool
}

var (
	reSeasonEp   = regexp.MustCompile(`(?i)\bS(\d{1,2})[\s._-]?E(\d{1,3})\b`)
	reSeasonEp2  = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
	reParenYear  = regexp.MustCompile(`\((19|20)\d{2}\)`)
	reYear       = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	reSeasonDir  = regexp.MustCompile(`(?i)^(?:season|s)[ ._-]?(\d{1,2})$`)
	reLeadingNum = regexp.MustCompile(`^(\d{1,3})[\s._-]+`)
	reEpisodePre = regexp.MustCompile(`(?i)^[\s._-]*(?:e|ep|episode)[ ._-]?(\d{1,3})\b`)
	reYearOnly   = regexp.MustCompile(`^(19|20)\d{2}$`)
)

// tagList holds release tags in canonical form; separators inside each tag
// may be any of . _ - or space in the filename (e.g. "H.264", "WEB-DL", "DDP5.1").
var tagList = []string{
	"1080p", "720p", "2160p", "480p", "360p", "4k", "uhd", "hdr", "hdr10", "webrip", "webdl", "web-dl",
	"hdtv", "bluray", "blu-ray", "bdrip", "bd25", "bd50", "dvdrip", "dvdscr", "dvd", "hdrip",
	"remux", "repack", "proper", "extended", "uncut", "unrated", "remastered", "multi", "dual",
	"dubbed", "subbed", "subs", "x264", "x265", "h264", "h.264", "h265", "h.265", "hevc", "avc",
	"xvid", "divx", "aac", "ac3", "eac3", "ddp", "ddp5.1", "ddp7.1", "dd5.1", "dd7.1", "dts",
	"dtshd", "dts-hd", "truehd", "atmos", "amzn", "nf", "netflix", "dsnp", "hmax", "hulu", "bbc",
	"itv", "rarbg", "ettv", "yts", "yify", "psa", "tgx", "10bit", "8bit", "hddvd", "dolby",
	"digital", "2.0", "5.1", "7.1",
}

var tagRe = buildTagRe()

func buildTagRe() *regexp.Regexp {
	parts := make([]string, len(tagList))
	for i, tag := range tagList {
		var b strings.Builder
		for _, r := range tag {
			if r == '.' || r == '-' {
				b.WriteString(`[._\-\s]?`)
			} else {
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		parts[i] = b.String()
	}
	return regexp.MustCompile(`(?i)(^|[^a-z0-9])(?:` + strings.Join(parts, "|") + `)([^a-z0-9]|$)`)
}

// stripTags removes release tags while preserving the separator characters on
// both sides so consecutive tags still match on the next pass.
func stripTags(s string) string {
	for n := 0; n < 10; n++ {
		locs := tagRe.FindAllStringSubmatchIndex(s, -1)
		if len(locs) == 0 {
			return s
		}
		var b strings.Builder
		last := 0
		for _, loc := range locs {
			b.WriteString(s[last:loc[0]])
			b.WriteString(s[loc[2]:loc[3]]) // left boundary
			b.WriteString(s[loc[4]:loc[5]]) // right boundary
			last = loc[1]
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return s
}

func dotsToSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '_' {
			return ' '
		}
		return r
	}, s)
}

var stopwords = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "part": true, "movie": true, "film": true, "edit": true, "cut": true, "version": true, "one": true, "two": true, "three": true}

func looksLikeGroupToken(tail string) bool {
	if tail == "" || strings.Contains(tail, " ") || len(tail) > 16 || stopwords[strings.ToLower(tail)] {
		return false
	}
	hasUpper, hasLower, hasDigit := false, false, false
	for _, r := range tail {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if hasDigit && !hasUpper && !hasLower {
		return false // plain numbers are often real titles/years
	}
	return (hasUpper && hasLower) || (hasUpper && !hasLower)
}

// stripTrailingGroup removes a release-group suffix like "... -iVy" or
// "...-GRP" while protecting "Spider-Man" and "- The Movie".
func stripTrailingGroup(s string) string {
	if idx := strings.LastIndex(s, " -"); idx >= 0 {
		tail := strings.TrimSpace(s[idx+2:])
		if looksLikeGroupToken(tail) {
			return strings.TrimSpace(s[:idx])
		}
		return s
	}
	if m := reAttachedGroup.FindStringSubmatch(s); m != nil {
		prefix := s[:len(s)-len(m[0])]
		if prefix != "" {
			last := prefix[len(prefix)-1]
			if last == '.' || last == '_' || last == '-' || (last >= '0' && last <= '9') {
				return strings.TrimSuffix(prefix, ".")
			}
		}
	}
	return s
}

var reAttachedGroup = regexp.MustCompile(`[-._][A-Za-z0-9]{1,15}$`)

// cleanTitle removes bracketed release groups and quality tags. Parenthesised
// groups containing a year are preserved (year extraction happens elsewhere).
func cleanTitle(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		r := s[i]
		if r == '[' || r == '(' || r == '{' {
			closer := byte(']')
			if r == '(' {
				closer = ')'
			} else if r == '{' {
				closer = '}'
			}
			if end := strings.IndexByte(s[i+1:], closer); end >= 0 {
				inner := s[i+1 : i+1+end]
				keep := r == '(' && reYearOnly.MatchString(strings.TrimSpace(inner))
				if keep {
					b.WriteString(s[i : i+end+2])
				}
				i = i + end + 2
				continue
			}
		}
		b.WriteByte(r)
		i++
	}
	s = stripTags(b.String())
	s = dotsToSpaces(s)
	out := strings.Join(strings.Fields(s), " ")
	out = strings.TrimSpace(strings.Trim(out, " -–"))
	return stripTrailingGroup(out)
}

func findYear(s string) int {
	if m := reParenYear.FindAllString(s, -1); len(m) > 0 {
		y, _ := strconv.Atoi(strings.Trim(m[len(m)-1], "()"))
		return y
	}
	matches := reYear.FindAllString(s, -1)
	if len(matches) == 0 {
		return 0
	}
	y, _ := strconv.Atoi(matches[len(matches)-1])
	return y
}

func stripYear(s string) string {
	s = reParenYear.ReplaceAllString(s, " ")
	s = reYear.ReplaceAllString(s, " ")
	return cleanTitle(s)
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// Parse derives metadata from a media path relative to the library root,
// using "Movies/" and "TV Shows/" layout conventions when present.
func Parse(relPath string) Parsed {
	parts := strings.Split(strings.Trim(relPath, "/"), "/")
	base := parts[len(parts)-1]
	name := base
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	name = dotsToSpaces(name)

	folders := parts[:len(parts)-1]
	topLevel := ""
	if len(folders) > 0 {
		topLevel = folders[0]
	}
	inTVRoot := strings.Contains(strings.ToLower(topLevel), "tv")

	var p Parsed

	if m := reSeasonEp.FindStringSubmatch(name); m != nil {
		p.IsTV = true
		p.Season, _ = strconv.Atoi(m[1])
		p.Episode, _ = strconv.Atoi(m[2])
		idx := strings.Index(strings.ToLower(name), strings.ToLower(m[0]))
		p.ShowTitle = cleanTitle(name[:idx])
		p.Title = strings.TrimLeft(cleanTitle(name[idx+len(m[0]):]), " -–_")
		p.Year = findYear(name)
	} else if m := reSeasonEp2.FindStringSubmatch(name); m != nil {
		p.IsTV = true
		p.Season, _ = strconv.Atoi(m[1])
		p.Episode, _ = strconv.Atoi(m[2])
		idx := strings.Index(name, m[0])
		p.ShowTitle = cleanTitle(name[:idx])
		p.Title = strings.TrimLeft(cleanTitle(name[idx+len(m[0]):]), " -–_")
		p.Year = findYear(name)
	} else {
		// Season folder + leading episode number, e.g. "Season 01/03 - Pilot.mkv".
		for i := len(folders) - 1; i >= 0; i-- {
			if m := reSeasonDir.FindStringSubmatch(folders[i]); m != nil {
				p.Season, _ = strconv.Atoi(m[1])
				if mm := reLeadingNum.FindStringSubmatch(name); mm != nil {
					p.IsTV = true
					p.Episode, _ = strconv.Atoi(mm[1])
					p.Title = cleanTitle(name[len(mm[0]):])
				} else if mm := reEpisodePre.FindStringSubmatch(name); mm != nil {
					p.IsTV = true
					p.Episode, _ = strconv.Atoi(mm[1])
					p.Title = cleanTitle(name[len(mm[0]):])
				}
				if i > 0 {
					p.ShowTitle = stripYear(cleanTitle(folders[i-1]))
				}
				break
			}
		}
	}

	if !p.IsTV {
		p.Title = stripYear(name)
		p.Year = findYear(name)
		if p.Year == 0 && len(folders) > 0 {
			p.Year = findYear(folders[len(folders)-1])
		}
		if p.Title == "" && len(folders) > 0 {
			p.Title = stripYear(cleanTitle(folders[len(folders)-1]))
		}
	}

	if p.IsTV {
		if len(folders) > 0 {
			cand := stripYear(cleanTitle(folders[len(folders)-1]))
			if reSeasonDir.MatchString(strings.ToLower(cand)) && len(folders) > 1 {
				cand = stripYear(cleanTitle(folders[len(folders)-2]))
			}
			if cand != "" && (p.ShowTitle == "" || inTVRoot) {
				p.ShowTitle = cand
			}
		}
		if p.ShowTitle == "" {
			p.ShowTitle = stripYear(name)
		}
		if p.Title == p.ShowTitle {
			p.Title = ""
		}
		if p.Year == 0 {
			for i := len(folders) - 1; i >= 0; i-- {
				if y := findYear(folders[i]); y != 0 {
					p.Year = y
					break
				}
			}
		}
	}

	p.Title = titleCase(strings.TrimSpace(p.Title))
	p.ShowTitle = titleCase(strings.TrimSpace(p.ShowTitle))
	if p.Title == "" && !p.IsTV {
		p.Title = titleCase(stripYear(name))
	}
	return p
}

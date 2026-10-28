// Package scan discovers library media and derives metadata from file paths.
package scan

import (
	"regexp"
	"strconv"
	"strings"
)

// Parsed holds metadata derived from a media path relative to the library root.
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
	hasUpper, hasLower, hasDigit := charClasses(tail)
	if hasDigit && !hasUpper && !hasLower {
		return false // plain numbers are often real titles/years
	}
	return (hasUpper && hasLower) || (hasUpper && !hasLower)
}

// charClasses inspects a token and reports whether it contains upper case
// letters, lower case letters, and digits.
func charClasses(tail string) (hasUpper, hasLower, hasDigit bool) {
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
	return hasUpper, hasLower, hasDigit
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
			var closer byte
			switch r {
			case '[':
				closer = ']'
			case '(':
				closer = ')'
			default: // '{'
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

// atoiSafe parses an integer, returning 0 on failure. Regex-captured groups
// only ever contain digits, so a failure here is an impossible edge case;
// treating it as absent metadata keeps the call sites clean.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func findYear(s string) int {
	if m := reParenYear.FindAllString(s, -1); len(m) > 0 {
		return atoiSafe(strings.Trim(m[len(m)-1], "()"))
	}
	matches := reYear.FindAllString(s, -1)
	if len(matches) == 0 {
		return 0
	}
	return atoiSafe(matches[len(matches)-1])
}

var reTrailingYear = regexp.MustCompile(`[ ._-](?:19|20)\d{2}\s*$`)

// stripYear removes parenthesised years and a trailing year, but only when the
// trailing year matches the release year — keeping "Blade Runner 2049 (2017)"
// and "1917 (2019)" intact while cleaning "The Matrix 1999.1080p".
func stripYear(s string, year int) string {
	s = reParenYear.ReplaceAllString(s, " ")
	s = cleanTitle(s)
	if year > 0 {
		re := regexp.MustCompile(`[ ._-]` + strconv.Itoa(year) + `\s*$`)
		s = strings.TrimSpace(strings.TrimRight(re.ReplaceAllString(s, " "), " -–"))
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.TrimRight(reTrailingYear.ReplaceAllString(s, " "), " -–"))
}

// bareYearTitle recovers numeric-only titles ("1984") from a filename.
func bareYearTitle(s string) string {
	t := strings.TrimSpace(reParenYear.ReplaceAllString(cleanTitle(s), ""))
	return strings.TrimSpace(strings.Trim(t, " -–"))
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// parseEpisodePatterns inspects the base filename for season/episode patterns
// like "S01E02" or "1x03" and fills in TV metadata when matched.
func parseEpisodePatterns(name string) (Parsed, bool) {
	if m := reSeasonEp.FindStringSubmatch(name); m != nil {
		var p Parsed
		p.IsTV = true
		p.Season = atoiSafe(m[1])
		p.Episode = atoiSafe(m[2])
		idx := strings.Index(strings.ToLower(name), strings.ToLower(m[0]))
		p.ShowTitle = cleanTitle(name[:idx])
		p.Title = strings.TrimLeft(cleanTitle(name[idx+len(m[0]):]), " -–_")
		p.Year = findYear(name)
		return p, true
	}
	if m := reSeasonEp2.FindStringSubmatch(name); m != nil {
		var p Parsed
		p.IsTV = true
		p.Season = atoiSafe(m[1])
		p.Episode = atoiSafe(m[2])
		idx := strings.Index(name, m[0])
		p.ShowTitle = cleanTitle(name[:idx])
		p.Title = strings.TrimLeft(cleanTitle(name[idx+len(m[0]):]), " -–_")
		p.Year = findYear(name)
		return p, true
	}
	return Parsed{}, false
}

// parseSeasonFolderTV detects "Season 01/03 - Pilot.mkv" style layouts where a
// season directory provides the season number and the filename provides the
// episode number.
func parseSeasonFolderTV(name string, folders []string) Parsed {
	var p Parsed
	for i := len(folders) - 1; i >= 0; i-- {
		if m := reSeasonDir.FindStringSubmatch(folders[i]); m != nil {
			p.Season = atoiSafe(m[1])
			p.IsTV = true
			if mm := reLeadingNum.FindStringSubmatch(name); mm != nil {
				p.Episode = atoiSafe(mm[1])
				p.Title = cleanTitle(name[len(mm[0]):])
			} else if mm := reEpisodePre.FindStringSubmatch(name); mm != nil {
				p.Episode = atoiSafe(mm[1])
				p.Title = cleanTitle(name[len(mm[0]):])
			} else {
				p.Title = cleanTitle(name)
			}
			if i > 0 {
				p.ShowTitle = stripYear(cleanTitle(folders[i-1]), findYear(folders[i-1]))
			}
			break
		}
	}
	return p
}

// parseMovieTitle derives movie metadata when no TV pattern matched: the year
// comes from the filename or the last folder, and the title from the cleaned
// name with the year stripped.
func parseMovieTitle(p Parsed, name string, folders []string) Parsed {
	p.Year = findYear(name)
	if p.Year == 0 && len(folders) > 0 {
		p.Year = findYear(folders[len(folders)-1])
	}
	p.Title = stripYear(name, p.Year)
	if p.Title == "" {
		p.Title = bareYearTitle(name)
	}
	if p.Title == "" && len(folders) > 0 {
		p.Title = stripYear(cleanTitle(folders[len(folders)-1]), p.Year)
	}
	return p
}

// enrichShowTitle backs the show title out of parent folders when the filename
// did not yield one, then falls back to the cleaned filename. It also clears a
// title equal to the show title and recovers the year from parent folders.
func enrichShowTitle(p Parsed, name string, folders []string, inTVRoot bool) Parsed {
	if len(folders) > 0 {
		cand := stripYear(cleanTitle(folders[len(folders)-1]), findYear(folders[len(folders)-1]))
		if reSeasonDir.MatchString(strings.ToLower(cand)) && len(folders) > 1 {
			cand = stripYear(cleanTitle(folders[len(folders)-2]), findYear(folders[len(folders)-2]))
		}
		if cand != "" && (p.ShowTitle == "" || inTVRoot) {
			p.ShowTitle = cand
		}
	}
	if p.ShowTitle == "" {
		p.ShowTitle = stripYear(name, p.Year)
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
	return p
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
	if tv, matched := parseEpisodePatterns(name); matched {
		p = tv
	} else {
		p = parseSeasonFolderTV(name, folders)
	}

	if !p.IsTV {
		p = parseMovieTitle(p, name, folders)
	}
	if p.IsTV {
		p = enrichShowTitle(p, name, folders, inTVRoot)
	}

	p.Title = titleCase(strings.TrimSpace(p.Title))
	p.ShowTitle = titleCase(strings.TrimSpace(p.ShowTitle))
	if p.Title == "" && !p.IsTV {
		p.Title = titleCase(stripYear(name, p.Year))
	}
	return p
}

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

var releaseTags = map[string]bool{
	"1080p": true, "720p": true, "2160p": true, "480p": true, "360p": true, "4k": true, "uhd": true,
	"hdr": true, "hdr10": true, "dv": true, "webrip": true, "webdl": true, "web": true, "hdtv": true,
	"bluray": true, "blu": true, "ray": true, "bdrip": true, "bd25": true, "bd50": true, "dvdrip": true,
	"dvdscr": true, "dvd": true, "hdrip": true, "remux": true, "repack": true, "proper": true,
	"extended": true, "uncut": true, "unrated": true, "remastered": true, "multi": true, "dual": true,
	"dubbed": true, "subbed": true, "subs": true, "x264": true, "x265": true, "h264": true, "h265": true,
	"hevc": true, "avc": true, "xvid": true, "divx": true, "aac": true, "ac3": true, "eac3": true,
	"ddp": true, "dd": true, "dts": true, "dtshd": true, "truehd": true, "atmos": true, "amzn": true,
	"nf": true, "netflix": true, "dsnp": true, "hmax": true, "hulu": true, "max": true, "bbc": true,
	"itv": true, "rarbg": true, "ettv": true, "yts": true, "yify": true, "psa": true, "tgx": true,
	"10bit": true, "8bit": true, "hddvd": true, "ipt": true,
}

var reBitrateTag = regexp.MustCompile(`^(ddp?|dd\+)?[57][\.,]?[17]$`)

func isReleaseTag(tok string) bool {
	t := strings.ToLower(strings.Trim(tok, ".,-_"))
	if t == "" {
		return false
	}
	return releaseTags[t] || reBitrateTag.MatchString(t)
}

func dotsToSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '_' {
			return ' '
		}
		return r
	}, s)
}

// cleanTitle removes bracketed release groups and quality tags. Parenthesised
// groups containing a year are preserved (year extraction happens elsewhere).
func cleanTitle(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		r := s[i]
		if r == '[' || r == '(' {
			closer := byte(']')
			if r == '(' {
				closer = ')'
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
	s = dotsToSpaces(b.String())
	var keep []string
	for _, f := range strings.Fields(s) {
		if !isReleaseTag(f) {
			keep = append(keep, f)
		}
	}
	out := strings.Join(keep, " ")
	return strings.TrimSpace(strings.Trim(out, " -–"))
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
	} else if m := reSeasonEp2.FindStringSubmatch(name); m != nil {
		p.IsTV = true
		p.Season, _ = strconv.Atoi(m[1])
		p.Episode, _ = strconv.Atoi(m[2])
		idx := strings.Index(name, m[0])
		p.ShowTitle = cleanTitle(name[:idx])
		p.Title = strings.TrimLeft(cleanTitle(name[idx+len(m[0]):]), " -–_")
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
	}

	p.Title = titleCase(strings.TrimSpace(p.Title))
	p.ShowTitle = titleCase(strings.TrimSpace(p.ShowTitle))
	if p.Title == "" && !p.IsTV {
		p.Title = titleCase(stripYear(name))
	}
	return p
}
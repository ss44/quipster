package subs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asticode/go-astisub"
)

type Cue struct {
	Start float64
	End   float64
	Text  string
}

var sidecarExts = []string{".srt", ".vtt"}

// PickSidecar finds the best English sidecar subtitle for a video:
// explicit en/eng/english tags first, then untagged files.
func PickSidecar(videoPath string) string {
	dir := filepath.Dir(videoPath)
	base := filepath.Base(videoPath)
	base = base[:len(base)-len(filepath.Ext(base))]
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type cand struct {
		path  string
		score int
	}
	var best *cand
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !sidecarExtsContain(ext) {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base+".")) && !strings.EqualFold(name, base+ext) {
			continue
		}
		mid := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(name), strings.ToLower(base)), ext))
		mid = strings.Trim(mid, ".")
		tokens := map[string]bool{}
		for _, t := range strings.Split(mid, ".") {
			if t != "" {
				tokens[t] = true
			}
		}
		score := -1
		switch {
		case tokens["en"] || tokens["eng"] || tokens["english"]:
			score = 2
		case len(tokens) == 0:
			score = 1
		default:
			continue // non-English tagged (forced/sdh/etc. also excluded)
		}
		if ext == ".srt" {
			score++ // prefer srt on ties
		}
		c := cand{path: filepath.Join(dir, name), score: score}
		if best == nil || c.score > best.score {
			b := c
			best = &b
		}
	}
	if best == nil || best.score < 1 {
		return ""
	}
	return best.path
}

func sidecarExtsContain(ext string) bool {
	for _, e := range sidecarExts {
		if e == ext {
			return true
		}
	}
	return false
}

// EnglishStream picks the first English-tagged embedded text stream, falling
// back to an untagged one only when it is the only stream. It returns the
// stream's position in the subtitle list, or -1 when none qualifies.
func EnglishStream(langs []string) int {
	untagged := -1
	for i, l := range langs {
		if isEnglish(l) {
			return i
		}
		if l == "" && untagged == -1 {
			untagged = i
		}
	}
	if untagged != -1 && len(langs) == 1 {
		return untagged
	}
	return -1
}

func isEnglish(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return l == "en" || l == "eng" || strings.HasPrefix(l, "en-")
}

var textTagReplacer = strings.NewReplacer("\r", "")

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Strip residual markup astisub did not consume.
	s = stripTags(s)
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return strings.Join(lines, " ")
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
				continue
			}
			b.WriteByte('>')
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// ParseFile parses an SRT or WebVTT file into normalized cues.
func ParseFile(path string) ([]Cue, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var subs *astisub.Subtitles
	switch strings.ToLower(filepath.Ext(path)) {
	case ".vtt":
		subs, err = astisub.ReadFromWebVTT(f)
	default:
		subs, err = astisub.ReadFromSRT(f)
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return fromAstisub(subs), nil
}

// ParseBytes parses raw SRT/VTT content by extension hint.
func ParseBytes(data []byte, ext string) ([]Cue, error) {
	var subs *astisub.Subtitles
	var err error
	switch strings.ToLower(ext) {
	case ".vtt":
		subs, err = astisub.ReadFromWebVTT(strings.NewReader(textTagReplacer.Replace(string(data))))
	default:
		subs, err = astisub.ReadFromSRT(strings.NewReader(textTagReplacer.Replace(string(data))))
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s content: %w", ext, err)
	}
	return fromAstisub(subs), nil
}

func fromAstisub(subs *astisub.Subtitles) []Cue {
	var out []Cue
	for _, it := range subs.Items {
		if it.StartAt < 0 || it.EndAt <= it.StartAt {
			continue
		}
		var parts []string
		for _, ln := range it.Lines {
			txt := strings.TrimSpace(ln.String())
			if txt != "" {
				parts = append(parts, txt)
			}
		}
		text := normalizeText(strings.Join(parts, " "))
		if text == "" {
			continue
		}
		start := it.StartAt.Seconds()
		end := it.EndAt.Seconds()
		// astisub may emit negative/zero-width items; clamp minimally.
		if end <= start {
			end = start + 0.5
		}
		out = append(out, Cue{Start: start, End: end, Text: text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// Offset shifts all cues by the given seconds (used for container start_time drift).
func Offset(cues []Cue, offset float64) []Cue {
	for i := range cues {
		cues[i].Start += offset
		cues[i].End += offset
		if cues[i].Start < 0 {
			cues[i].Start = 0
		}
	}
	return cues
}

package chunks

import (
	"strings"
)

const (
	maxChars    = 280
	maxCues     = 3
	gapCloseSec = 5.0
)

type Cue struct {
	Idx      int
	StartSec float64
	EndSec   float64
	Text     string
}

type Chunk struct {
	StartCueIdx int
	EndCueIdx   int
	Text        string
}

// Build merges consecutive cues into non-overlapping chunks of at most ~280
// characters or 3 cues. A large silence gap closes the chunk so search hits
// map to a watchable span.
func Build(cues []Cue) []Chunk {
	var out []Chunk
	var cur []Cue
	var chars int

	flush := func() {
		if len(cur) == 0 {
			return
		}
		texts := make([]string, len(cur))
		for i, c := range cur {
			texts[i] = c.Text
		}
		out = append(out, Chunk{
			StartCueIdx: cur[0].Idx,
			EndCueIdx:   cur[len(cur)-1].Idx,
			Text:        strings.Join(texts, " "),
		})
		cur = nil
		chars = 0
	}

	for _, c := range cues {
		t := strings.TrimSpace(c.Text)
		if t == "" {
			continue
		}
		if len(cur) > 0 {
			gap := c.StartSec - cur[len(cur)-1].EndSec
			tooLong := chars+1+len(t) > maxChars && len(cur) > 0
			tooMany := len(cur) >= maxCues
			if gap > gapCloseSec || tooLong || tooMany {
				flush()
			}
		}
		cur = append(cur, Cue{Idx: c.Idx, StartSec: c.StartSec, EndSec: c.EndSec, Text: t})
		chars += len(t) + 1
	}
	flush()
	return out
}

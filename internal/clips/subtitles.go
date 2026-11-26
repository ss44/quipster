package clips

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"quipster/internal/fonts"
)

const (
	assPlayResY   = 720
	assMinPlayRes = 320
	assMaxPlayRes = 2560
	assMarginH    = 48
	assBoxPadding = 10
)

// SubtitleCue is one timed subtitle line in source media seconds used when
// burning subtitles into a rendered clip.
type SubtitleCue struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// SubtitleStyle configures how burned-in subtitles are rendered via FFmpeg's
// libass filter. All pixel dimensions live in a canonical PlayResY=720 space
// so the live browser overlay and exported MP4/GIF renders stay proportional
// across resolutions.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type SubtitleStyle struct {
	Enabled      bool    `json:"enabled"`
	Font         string  `json:"font,omitempty"`
	FontSize     int     `json:"fontSize"`
	Color        string  `json:"color,omitempty"`
	Outline      float64 `json:"outline"`
	OutlineColor string  `json:"outlineColor,omitempty"`
	Shadow       float64 `json:"shadow"`
	BgOpacity    float64 `json:"bgOpacity"`
	BgColor      string  `json:"bgColor,omitempty"`
	Position     string  `json:"position,omitempty"`
	MarginV      int     `json:"marginV"`
	Bold         bool    `json:"bold,omitempty"`
	Italic       bool    `json:"italic,omitempty"`
	Uppercase    bool    `json:"uppercase,omitempty"`
}

// DefaultSubtitleStyle returns the baseline enabled subtitle style: clean
// white Inter text at bottom center with a crisp black outline and subtle
// drop shadow.
func DefaultSubtitleStyle() SubtitleStyle {
	return SubtitleStyle{
		Enabled:      true,
		Font:         fonts.DefaultID,
		FontSize:     40,
		Color:        "ffffff",
		Outline:      2.5,
		OutlineColor: "000000",
		Shadow:       1.5,
		BgOpacity:    0,
		BgColor:      "000000",
		Position:     "bottom",
		MarginV:      36,
	}
}

// NormalizeSubtitleStyle validates and canonicalizes a subtitle style so
// invalid inputs are rejected early and equivalent styles hash identically.
func NormalizeSubtitleStyle(in SubtitleStyle) (SubtitleStyle, error) {
	if !in.Enabled {
		return SubtitleStyle{}, nil
	}
	out := in
	if strings.TrimSpace(out.Font) == "" {
		out.Font = fonts.DefaultID
	}
	f, ok := fonts.Lookup(out.Font)
	if !ok {
		return SubtitleStyle{}, fmt.Errorf("unknown subtitle font %q", out.Font)
	}
	out.Font = f.ID

	if out.FontSize == 0 {
		out.FontSize = 40
	}
	if err := validateStyleColors(&out); err != nil {
		return SubtitleStyle{}, err
	}
	if err := validateStyleBounds(&out); err != nil {
		return SubtitleStyle{}, err
	}

	out.Position = strings.ToLower(strings.TrimSpace(out.Position))
	if out.Position == "" {
		out.Position = "bottom"
	}
	switch out.Position {
	case "bottom", "middle", "top":
	default:
		return SubtitleStyle{}, fmt.Errorf("subtitle position must be bottom, middle or top")
	}

	out.Outline = math.Round(out.Outline*10) / 10
	out.Shadow = math.Round(out.Shadow*10) / 10
	out.BgOpacity = math.Round(out.BgOpacity*100) / 100
	return out, nil
}

func validateStyleColors(out *SubtitleStyle) error {
	var err error
	if out.Color, err = normalizeHexColor(out.Color, "ffffff"); err != nil {
		return fmt.Errorf("invalid subtitle color: %w", err)
	}
	if out.OutlineColor, err = normalizeHexColor(out.OutlineColor, "000000"); err != nil {
		return fmt.Errorf("invalid subtitle outlineColor: %w", err)
	}
	if out.BgColor, err = normalizeHexColor(out.BgColor, "000000"); err != nil {
		return fmt.Errorf("invalid subtitle bgColor: %w", err)
	}
	return nil
}

func validateStyleBounds(out *SubtitleStyle) error {
	if out.FontSize < 16 || out.FontSize > 96 {
		return fmt.Errorf("subtitle fontSize %d out of range (16-96)", out.FontSize)
	}
	if out.Outline < 0 || out.Outline > 6 {
		return fmt.Errorf("subtitle outline %.1f out of range (0-6)", out.Outline)
	}
	if out.Shadow < 0 || out.Shadow > 6 {
		return fmt.Errorf("subtitle shadow %.1f out of range (0-6)", out.Shadow)
	}
	if out.BgOpacity < 0 || out.BgOpacity > 1 {
		return fmt.Errorf("subtitle bgOpacity %.2f out of range (0-1)", out.BgOpacity)
	}
	if out.MarginV < 0 || out.MarginV > 220 {
		return fmt.Errorf("subtitle marginV %d out of range (0-220)", out.MarginV)
	}
	return nil
}

// CacheToken returns a canonical cache-key segment for enabled subtitle
// styles, or "" when subtitles are disabled.
func (s *SubtitleStyle) CacheToken() string {
	if s == nil || !s.Enabled {
		return ""
	}
	n, err := NormalizeSubtitleStyle(*s)
	if err != nil {
		n = *s
	}
	return fmt.Sprintf("subs:v1|%s|%d|%s|%.1f|%s|%.1f|%.2f|%s|%s|%d|%t|%t|%t",
		n.Font, n.FontSize, n.Color, n.Outline, n.OutlineColor, n.Shadow,
		n.BgOpacity, n.BgColor, n.Position, n.MarginV, n.Bold, n.Italic, n.Uppercase)
}

func normalizeHexColor(raw, fallback string) (string, error) {
	v := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
	if v == "" {
		return fallback, nil
	}
	if len(v) != 6 {
		return "", fmt.Errorf("%q must be a 6-digit hex color", raw)
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%q must be a 6-digit hex color", raw)
		}
	}
	return v, nil
}

// assColor formats a 6-digit RGB hex string and opacity (0..1) into ASS
// &HAABBGGRR format, where alpha 00 is opaque and FF is transparent.
func assColor(rgbHex string, opacity float64) string {
	if opacity < 0 {
		opacity = 0
	}
	if opacity > 1 {
		opacity = 1
	}
	alpha := int(math.Round((1 - opacity) * 255))
	r, g, b := rgbHex[0:2], rgbHex[2:4], rgbHex[4:6]
	return fmt.Sprintf("&H%02X%s%s%s", alpha, strings.ToUpper(b), strings.ToUpper(g), strings.ToUpper(r))
}

func assPlayResX(width, height int) int {
	if width <= 0 || height <= 0 {
		return 1280
	}
	x := int(math.Round(float64(assPlayResY) * float64(width) / float64(height)))
	if x < assMinPlayRes {
		return assMinPlayRes
	}
	if x > assMaxPlayRes {
		return assMaxPlayRes
	}
	return x
}

func assAlignment(pos string) int {
	switch pos {
	case "top":
		return 8
	case "middle":
		return 5
	default:
		return 2
	}
}

func assTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	totalCs := int(math.Round(sec * 100))
	cs := totalCs % 100
	totalSec := totalCs / 100
	s := totalSec % 60
	totalMin := totalSec / 60
	m := totalMin % 60
	h := totalMin / 60
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

func escapeASSText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "{", `\{`)
	s = strings.ReplaceAll(s, "}", `\}`)
	s = strings.ReplaceAll(s, "\n", `\N`)
	return s
}

// BuildASS renders an Advanced SubStation Alpha script for cues shifted to
// clip-relative time [0, clipDur]. mediaWidth/mediaHeight set PlayResX to
// match the video's aspect ratio so libass never stretches glyphs on 4:3 or
// CinemaScope sources.
func BuildASS(style SubtitleStyle, cues []SubtitleCue, clipStart, clipDur float64, mediaWidth, mediaHeight int) string {
	n, err := NormalizeSubtitleStyle(style)
	if err != nil || !n.Enabled {
		n = DefaultSubtitleStyle()
	}
	font, ok := fonts.Lookup(n.Font)
	if !ok {
		font, _ = fonts.Lookup(fonts.DefaultID)
	}

	bold := 0
	if n.Bold {
		bold = -1
	}
	italic := 0
	if n.Italic {
		italic = -1
	}

	borderStyle := 1
	outlineColour := assColor(n.OutlineColor, 1)
	backColour := assColor("000000", 0.72)
	outline := n.Outline
	shadow := n.Shadow
	if n.BgOpacity > 0 {
		// In libass BorderStyle=3, OutlineColour fills the opaque/translucent
		// box and Outline controls the box padding around the glyphs.
		borderStyle = 3
		outlineColour = assColor(n.BgColor, n.BgOpacity)
		backColour = "&HFF000000"
		outline = assBoxPadding
		shadow = 0
	}

	playResX := assPlayResX(mediaWidth, mediaHeight)
	var b strings.Builder
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	fmt.Fprintf(&b, "PlayResX: %d\n", playResX)
	fmt.Fprintf(&b, "PlayResY: %d\n", assPlayResY)
	b.WriteString("ScaledBorderAndShadow: yes\n")
	b.WriteString("WrapStyle: 0\n\n")

	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,%s,&H00FFFFFF,%s,%s,%d,%d,0,0,100,100,0,0,%d,%.1f,%.1f,%d,%d,%d,%d,1\n\n",
		font.Family,
		n.FontSize,
		assColor(n.Color, 1),
		outlineColour,
		backColour,
		bold,
		italic,
		borderStyle,
		outline,
		shadow,
		assAlignment(n.Position),
		assMarginH,
		assMarginH,
		n.MarginV,
	)

	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, c := range cues {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		start := c.Start - clipStart
		end := c.End - clipStart
		if end <= 0 || start >= clipDur {
			continue
		}
		if start < 0 {
			start = 0
		}
		if end > clipDur {
			end = clipDur
		}
		if end <= start {
			continue
		}
		if n.Uppercase {
			text = strings.ToUpper(text)
		}
		//nolint:misspell // ASS format requires the literal "Dialogue:" event keyword.
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
			assTimestamp(start), assTimestamp(end), escapeASSText(text))
	}
	return b.String()
}

// escapeFilterPath escapes a filesystem path for use inside a single-quoted
// FFmpeg filter option value (e.g. ass=filename='...':fontsdir='...').
func escapeFilterPath(p string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`:`, `\:`,
		`'`, `\'`,
		`,`, `\,`,
		`;`, `\;`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return r.Replace(p)
}

// prepareSubtitleFilter writes a temporary ASS file and ensures embedded fonts
// are materialized on disk, returning the FFmpeg ass filter expression and a
// cleanup callback for the temporary ASS file.
func (s *Renderer) prepareSubtitleFilter(style *SubtitleStyle, cues []SubtitleCue, clipStart, clipDur float64, width, height int) (string, func(), error) {
	noop := func() {}
	if style == nil || !style.Enabled || len(cues) == 0 {
		return "", noop, nil
	}
	fontsDir, err := fonts.Materialize(filepath.Join(s.cfg.CacheDir, "fonts"))
	if err != nil {
		return "", noop, fmt.Errorf("prepare subtitle fonts: %w", err)
	}
	assDir := filepath.Join(s.cfg.CacheDir, "clips")
	if mkErr := os.MkdirAll(assDir, 0o750); mkErr != nil {
		return "", noop, fmt.Errorf("create subtitle temp dir: %w", mkErr)
	}
	tmp, err := os.CreateTemp(assDir, "subs-*.ass")
	if err != nil {
		return "", noop, fmt.Errorf("create temp ass file: %w", err)
	}
	assPath := tmp.Name()
	cleanup := func() { removePartial(assPath) }
	content := BuildASS(*style, cues, clipStart, clipDur, width, height)
	if _, werr := tmp.WriteString(content); werr != nil {
		if cerr := tmp.Close(); cerr != nil {
			cleanup()
			return "", noop, cerr
		}
		cleanup()
		return "", noop, fmt.Errorf("write temp ass file: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		cleanup()
		return "", noop, fmt.Errorf("close temp ass file: %w", cerr)
	}
	filter := fmt.Sprintf("ass=filename='%s':fontsdir='%s'",
		escapeFilterPath(assPath), escapeFilterPath(fontsDir))
	return filter, cleanup, nil
}

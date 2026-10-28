// Package probe wraps ffprobe/ffmpeg to inspect media files, pick the best
// audio and text-subtitle tracks, and extract embedded subtitles to SRT.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Probing must read container headers, not crawl the media payload. Without
// caps, files with a trailing or missing index (MP4 with moov at the end, MKV
// without cues) can make ffprobe scan an entire 20GB+ file. These bounds keep
// probe time independent of file size (issue #1).
const (
	// defaultAnalyzeDuration caps ffprobe's per-stream analysis window
	// (flag value is microseconds).
	defaultAnalyzeDuration = 10 * time.Second
	// defaultProbeSize caps how many input bytes ffprobe inspects.
	defaultProbeSize = 50 << 20 // 50 MiB
	// defaultProbeTimeout bounds one ffprobe run before it is killed and the
	// file skipped.
	defaultProbeTimeout = 60 * time.Second
)

// SubtitleStream describes one embedded subtitle stream reported by ffprobe.
type SubtitleStream struct {
	Codec   string // subrip, mov_text, ass, ...
	Lang    string
	Index   int // absolute ffprobe stream index
	Ordinal int // index among subtitle streams only
}

// AudioStream describes one audio track reported by ffprobe.
type AudioStream struct {
	Codec    string
	Lang     string
	Title    string // track title, e.g. "Commentary"
	Index    int    // absolute ffprobe stream index
	Ordinal  int    // index among audio streams only
	Channels int
	Default  bool
}

type optimizationMetadata struct {
	videoBitRate  int64
	formatBitRate int64
	avgFrameRate  float64
	frameRate     float64
	maxBFrames    int
}

// Result is the probe summary of a media file: format metadata plus the
// audio and subtitle streams relevant for clip building.
type Result struct {
	VideoCodec string
	AudioCodec string
	Container  string
	Subs       []SubtitleStream
	Audio      []AudioStream
	Duration   float64
	Width      int
	Height     int
	StartTime  float64
	metadata   optimizationMetadata
}

// ffprobeOut mirrors the subset of ffprobe JSON output the pipeline consumes.
// Fields are ordered for compact layout; JSON decoding is name-based.
type ffprobeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		StartTime  string `json:"start_time"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Tags         map[string]string `json:"tags"`
		CodecName    string            `json:"codec_name"`
		CodecType    string            `json:"codec_type"`
		Duration     string            `json:"duration"`
		BitRate      string            `json:"bit_rate"`
		AvgFrameRate string            `json:"avg_frame_rate"`
		FrameRate    string            `json:"r_frame_rate"`
		Index        int               `json:"index"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		Channels     int               `json:"channels"`
		HasBFrames   int               `json:"has_b_frames"`
		Disposition  struct {
			Default int `json:"default"`
		} `json:"disposition"`
	} `json:"streams"`
}

// FfprobeBin resolves the ffprobe binary, overridable via FFPROBE_BIN.
func FfprobeBin() string { return envOr("FFPROBE_BIN", "ffprobe") }

// FfmpegBin resolves the ffmpeg binary, overridable via FFMPEG_BIN.
func FfmpegBin() string { return envOr("FFMPEG_BIN", "ffmpeg") }

func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func parseBitRate(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseRate(s string) float64 {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0
	}
	numerator, nerr := strconv.ParseFloat(parts[0], 64)
	denominator, derr := strconv.ParseFloat(parts[1], 64)
	if nerr != nil || derr != nil || denominator == 0 || numerator <= 0 {
		return 0
	}
	return numerator / denominator
}

func fileDuration(o *ffprobeOut) float64 {
	if o.Format.Duration != "" {
		if d, err := strconv.ParseFloat(o.Format.Duration, 64); err == nil {
			return d
		}
	}
	for _, s := range o.Streams {
		if s.CodecType == "video" && s.Duration != "" {
			if d, err := strconv.ParseFloat(s.Duration, 64); err == nil {
				return d
			}
		}
	}
	return 0
}

// probeArgs builds the ffprobe invocation: capped analysis (-analyzeduration,
// -probesize) so large files are probed from headers instead of scanned whole.
func probeArgs(path string) []string {
	return []string{
		"-v", "error",
		"-analyzeduration", strconv.FormatInt(int64(defaultAnalyzeDuration/time.Microsecond), 10),
		"-probesize", strconv.FormatInt(int64(defaultProbeSize), 10),
		"-print_format", "json",
		"-show_format", "-show_streams",
		"--", path,
	}
}

// stderrDetail surfaces ffprobe's stderr (-v error output) for diagnosis,
// capped since it is stored in the media error column shown in the UI.
func stderrDetail(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || len(ee.Stderr) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(ee.Stderr))
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "..."
	}
	return ": " + s
}

// Probe runs ffprobe and summarizes streams of interest. ctx must be non-nil
// and bounds the run: expiry kills ffprobe and returns a context error the
// caller can detect with errors.Is.
func Probe(ctx context.Context, path string) (*Result, error) {
	out, err := exec.CommandContext(ctx, FfprobeBin(), probeArgs(path)...).Output() //nolint:gosec // probing user-supplied media paths is the package's purpose
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("ffprobe %s: %w", path, cerr)
		}
		return nil, fmt.Errorf("ffprobe %s: %w%s", path, err, stderrDetail(err))
	}
	var o ffprobeOut
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("ffprobe parse %s: %w", path, err)
	}
	r := &Result{
		Duration:  fileDuration(&o),
		Container: o.Format.FormatName,
		metadata:  optimizationMetadata{formatBitRate: parseBitRate(o.Format.BitRate)},
	}
	// start_time is best-effort metadata; a missing or unparsable value
	// simply means no drift correction later.
	if st, err := strconv.ParseFloat(o.Format.StartTime, 64); err == nil {
		r.StartTime = st
	}
	ord := 0
	audioOrd := 0
	for _, s := range o.Streams {
		switch s.CodecType {
		case "video":
			if r.VideoCodec == "" {
				r.VideoCodec = s.CodecName
				r.Width = s.Width
				r.Height = s.Height
				r.metadata.videoBitRate = parseBitRate(s.BitRate)
				r.metadata.avgFrameRate = parseRate(s.AvgFrameRate)
				r.metadata.frameRate = parseRate(s.FrameRate)
				r.metadata.maxBFrames = s.HasBFrames
			}
		case "audio":
			r.Audio = append(r.Audio, AudioStream{
				Index:    s.Index,
				Ordinal:  audioOrd,
				Codec:    s.CodecName,
				Lang:     normalizeLang(s.Tags["language"]),
				Title:    s.Tags["title"],
				Default:  s.Disposition.Default == 1,
				Channels: s.Channels,
			})
			audioOrd++
		case "subtitle":
			r.Subs = append(r.Subs, SubtitleStream{
				Index:   s.Index,
				Ordinal: ord,
				Codec:   s.CodecName,
				Lang:    normalizeLang(s.Tags["language"]),
			})
			ord++
		}
	}
	if chosen := PickEnglishAudio(r.Audio); chosen != nil {
		r.AudioCodec = chosen.Codec
	}
	return r, nil
}

// PickEnglishAudio selects the audio track to use for clips. English-tagged
// tracks win (default disposition preferred), commentary/audio-description
// tracks are avoided, then the default track, then the first usable track.
func PickEnglishAudio(tracks []AudioStream) *AudioStream {
	if len(tracks) == 0 {
		return nil
	}
	usable := func(t AudioStream) bool {
		tl := strings.ToLower(t.Title)
		return !strings.Contains(tl, "commentary") && !strings.Contains(tl, "description")
	}
	var best *AudioStream
	for i := range tracks {
		t := &tracks[i]
		if usable(*t) && t.Lang == "en" {
			if best == nil || (t.Default && !best.Default) {
				best = t
			}
		}
	}
	if best != nil {
		return best
	}
	for i := range tracks {
		if usable(tracks[i]) && tracks[i].Default {
			return &tracks[i]
		}
	}
	for i := range tracks {
		if usable(tracks[i]) {
			return &tracks[i]
		}
	}
	return &tracks[0]
}

func normalizeLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	switch l {
	case "eng", "en":
		return "en"
	}
	return l
}

// textSubCodecs are subtitle codecs convertible to SRT; bitmap formats (pgs,
// vobsub) are skipped since conversion would require a full-file read and fail.
var textSubCodecs = map[string]bool{
	"subrip": true, "srt": true, "mov_text": true, "ass": true, "ssa": true,
	"webvtt": true, "text": true, "dvb_teletext": true,
}

// IsTextSubtitle reports whether the codec can be extracted to SRT.
func IsTextSubtitle(codec string) bool { return textSubCodecs[strings.ToLower(codec)] }

// ExtractSubtitle dumps one subtitle stream to SRT. Extraction requires
// scanning the whole container, so callers should bound ctx.
func ExtractSubtitle(ctx context.Context, path string, ordinal int, outPath string) error {
	cmd := exec.CommandContext(ctx, FfmpegBin(), "-v", "error", "-y", "-i", path, //nolint:gosec // extracting from user-supplied media paths is the package's purpose
		"-map", fmt.Sprintf("0:s:%d", ordinal), "-vn", "-an", "-c:s", "srt", outPath)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract subtitle stream %d: %w: %s", ordinal, err, strings.TrimSpace(string(b)))
	}
	return nil
}

package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type SubtitleStream struct {
	Index   int    // absolute ffprobe stream index
	Ordinal int    // index among subtitle streams only
	Codec   string // subrip, mov_text, ass, ...
	Lang    string
}

type AudioStream struct {
	Index    int // absolute ffprobe stream index
	Ordinal  int // index among audio streams only
	Codec    string
	Lang     string
	Title    string // track title, e.g. "Commentary"
	Default  bool
	Channels int
}

type Result struct {
	Duration   float64
	Width      int
	Height     int
	VideoCodec string
	AudioCodec string
	Container  string
	StartTime  float64
	Subs       []SubtitleStream
	Audio      []AudioStream
}

type ffprobeOut struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecName   string `json:"codec_name"`
		CodecType   string `json:"codec_type"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Channels    int    `json:"channels"`
		Duration    string `json:"duration"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		StartTime  string `json:"start_time"`
	} `json:"format"`
}

func FfprobeBin() string { return envOr("FFPROBE_BIN", "ffprobe") }

func FfmpegBin() string { return envOr("FFMPEG_BIN", "ffmpeg") }

func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
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

// Probe runs ffprobe and summarises streams of interest.
func Probe(path string) (*Result, error) {
	out, err := exec.Command(FfprobeBin(), "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "--", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var o ffprobeOut
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("ffprobe parse %s: %w", path, err)
	}
	r := &Result{
		Duration:  fileDuration(&o),
		Container: o.Format.FormatName,
	}
	if o.Format.StartTime != "" {
		r.StartTime, _ = strconv.ParseFloat(o.Format.StartTime, 64)
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
	cmd := exec.CommandContext(ctx, FfmpegBin(), "-v", "error", "-y", "-i", path,
		"-map", fmt.Sprintf("0:s:%d", ordinal), "-vn", "-an", "-c:s", "srt", outPath)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract subtitle stream %d: %w: %s", ordinal, err, strings.TrimSpace(string(b)))
	}
	return nil
}

package probe

import "math"

const highVideoBitRate = 40_000_000
const maxLowLatencyBFrames = 4
const frameRateTolerance = 0.05

func optimizationFlags(r *Result) []string {
	if r == nil {
		return nil
	}
	var flags []string
	if r.VideoCodec != "" && r.VideoCodec != "h264" {
		flags = append(flags, "video_codec_compatibility")
	}
	if r.AudioCodec != "" && r.AudioCodec != "aac" {
		flags = append(flags, "audio_codec_compatibility")
	}
	bitRate := r.metadata.videoBitRate
	if bitRate == 0 {
		bitRate = r.metadata.formatBitRate
	}
	if r.VideoCodec != "" && bitRate >= highVideoBitRate {
		flags = append(flags, "high_video_bitrate")
	}
	if r.metadata.avgFrameRate > 0 && r.metadata.frameRate > 0 && math.Abs(r.metadata.avgFrameRate-r.metadata.frameRate)/r.metadata.avgFrameRate > frameRateTolerance {
		flags = append(flags, "suspected_variable_frame_rate")
	}
	if r.metadata.maxBFrames > maxLowLatencyBFrames {
		flags = append(flags, "deep_b_frame_reordering")
	}
	return flags
}

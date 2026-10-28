# Media optimization diagnostics

Quipster's checks are advisory. Probe jobs read metadata, persist finding codes, and emit them to the diagnostic log; they never rewrite source media. A recommendation is a workflow hint, not proof that a file is defective. Keep originals and validate a derivative on the clients that matter.

## Metrics and interpretation

| Area | Inspect | Why it can matter | Heuristic |
|---|---|---|---|
| Container/index | Container, index/cue availability, packet timestamp continuity, and for ISO-BMFF/MP4 the `moov` atom position relative to `mdat` | Missing/late indexes can force scans; a late MP4 `moov` delays progressive startup. Neither atom order nor index completeness is reliably exposed by ordinary bounded `ffprobe -show_format/-show_streams` metadata. | Treat failed/slow seeks or absent index structures as a warning. A `moov` after media data is a progressive-startup warning; `-movflags +faststart` remuxes it to the front. The current Quipster probe does not claim to detect these conditions. |
| GOP / random access | Presentation timestamps of video keyframes; maximum keyframe-to-keyframe interval in seconds and frames; keyframe spacing variance | Input-side `-ss` seeks to a preceding seek point and decodes/discards forward. Long GOPs increase that work and can make clip boundaries less exact. | Informational at >2 seconds; recommend review at >5 seconds for frequent interactive clipping. Convert frames using the actual frame rate. Current bounded Quipster metadata probing does not scan packets/frames to calculate GOP length. |
| Video codec/profile | `codec_name`, `profile`, `level`, `pix_fmt`, bit depth, `has_b_frames`, stream bitrate | Unsupported profiles/codecs can force slow software decode or fail on target clients. B-frames require reordering but are normal in delivery encodes and are not independently a seekability defect. | Non-H.264 is a compatibility hint, not a general warning. More than 4 reported B-frames is shown only as a low-latency hint. Do not recommend removing B-frames unless the workflow has measured reorder latency. |
| Frame timing | `avg_frame_rate`, `r_frame_rate`, per-frame PTS/duration, MediaInfo `FrameRate_Mode` | Variable frame timing complicates timestamp-to-frame mapping and can change clip cadence; VFR is valid and is not inherently slower to seek. `r_frame_rate` is nominal and not proof of VFR. | A >5% divergence between average and nominal rates is only a suspicion. Confirm using frame timestamps or MediaInfo before CFR conversion. |
| Bitrate / I/O | Video `bit_rate`, format `bit_rate`, file size and duration; compare read throughput against the source bitrate | High bitrate can saturate network/NAS or storage and increase bytes read during decode/discard. Container bitrate includes audio and overhead. | Quipster flags a video or fallback format bitrate >=40 Mb/s as a storage-cost hint, not as evidence that bitrate is the bottleneck. Compare with measured source-read throughput first. |
| Audio | Audio `codec_name`, profile, `sample_rate`, `channels`, `channel_layout`, `bit_rate` | DTS/TrueHD and some multichannel layouts are unsupported by browsers/devices and may cause fallback/transcoding. Audio codec rarely controls video seek latency. | Quipster treats a selected non-AAC audio codec as a compatibility hint. Keep lossless/high-quality audio unless compatibility or bandwidth requires conversion. |
| Container overhead | Format size/bitrate versus elementary stream sizes/bitrates; packet count and muxing | Excessive small-packet or metadata overhead can add I/O and muxing work, but moderate overhead is normal and container choice alone does not diagnose poor seeking. | No universal percentage threshold. Investigate only when measured I/O or parsing is costly; remuxing may reduce overhead but will not fix a long GOP or unsupported codec. |

## Quipster warning rules

These rules use fields already returned by the normal bounded `ffprobe` metadata probe. They are persisted per media row and summarized by the admin Diagnostics view and report; warning counts are code counts, so one file can contribute to multiple codes.

- `video_codec_compatibility`: the first video stream is not H.264. Informational compatibility recommendation only; codecs such as HEVC, AV1, VP9, and ProRes may be preferable in environments that support them.
- `audio_codec_compatibility`: the selected audio stream is not AAC. This is about target playback compatibility, not seeking.
- `high_video_bitrate`: video stream bitrate (or, if missing, container bitrate) is at least 40,000,000 bits/s. This is a conservative storage/I/O hint.
- `suspected_variable_frame_rate`: positive average and nominal frame rates differ by more than 5%. Verify timestamps before converting.
- `deep_b_frame_reordering`: FFprobe reports more than four reordered B-frames. This is relevant only to strict low-latency decode; it is not a generic optimization recommendation.

The app does not currently infer GOP length, actual VFR, MP4 atom order, missing container indexes, or container overhead. Obtaining these reliably can require reading packets or scanning atom structures across large files, so those checks remain explicit operator diagnostics rather than an unbounded step in every library probe.

## Extracting the evidence

Use `ffprobe` JSON to inspect metadata without decoding the media payload:

```sh
ffprobe -v error -show_entries 'format=format_name,duration,size,bit_rate:stream=index,codec_type,codec_name,profile,level,bit_rate,avg_frame_rate,r_frame_rate,has_b_frames,nb_frames,time_base,pix_fmt,field_order,sample_rate,channels,channel_layout' -of json "input.mkv"
```

For MediaInfo's human-readable frame-rate mode and additional codec/container fields:

```sh
mediainfo --Output=JSON "input.mkv"
```

To inspect keyframe spacing, dump video frame keyframe flags and timestamps. This can read/decode a large file; consider testing a shorter interval first and do not treat a short sample as a maximum-GOP guarantee:

```sh
ffprobe -v error -select_streams v:0 -show_frames -show_entries 'frame=key_frame,best_effort_timestamp_time,pkt_duration_time' -of csv=p=0 "input.mkv"
```

A packet-level alternative exposes key packet flags and timestamps, but also requires a scan and timestamps can be absent or discontinuous:

```sh
ffprobe -v error -select_streams v:0 -show_packets -show_entries 'packet=pts_time,dts_time,duration_time,flags' -of csv=p=0 "input.mkv"
```

For MP4/QuickTime atom placement, trace output is version-dependent; inspect root-level `moov` and first `mdat` positions rather than parsing the prose as a stable API:

```sh
ffprobe -v trace -i "input.mp4" -show_format -show_streams -of json 2>atoms.log
```

A `moov` atom before `mdat` is suitable for progressive startup. Verify index/cue presence with a format-aware parser (for example, MP4 atom inspection or Matroska Cues inspection) and test an actual seek. Successful `ffprobe` output alone does not guarantee fast or accurate random access.

## Non-destructive remediation examples

All examples write a new file and leave the input intact. Choose a distinct output path, ensure sufficient free space, and review stream/subtitle mapping before using them on production media.

Move MP4 metadata to the front without re-encoding:

```sh
ffmpeg -i "input.mp4" -map 0 -c copy -movflags +faststart "output-faststart.mp4"
```

Remux to rebuild the destination container's indexes/cues where supported, without changing encoded streams:

```sh
ffmpeg -i "input.mkv" -map 0 -c copy "output-remuxed.mkv"
```

Create a broadly compatible H.264/AAC MP4 derivative. This example keeps the first video and all audio streams but omits subtitles, attachments, and data streams; add explicit maps/codecs if those are needed. Re-encoding video and audio is lossy and CPU-intensive:

```sh
ffmpeg -i "input.mkv" -map 0:v:0 -map 0:a? -c:v libx264 -preset medium -crf 20 -c:a aac -b:a 192k -movflags +faststart "output-compatible.mp4"
```

Use a shorter keyframe interval when interactive random access matters. Set `-g` to approximately two times the chosen output frame rate for a roughly two-second GOP (for example, 48 at 24 fps); scene-cut behavior and encoder-specific settings affect exact intervals:

```sh
ffmpeg -i "input.mkv" -map 0:v:0 -map 0:a? -c:v libx264 -preset medium -crf 20 -g 48 -keyint_min 48 -sc_threshold 0 -c:a copy "output-short-gop.mkv"
```

Convert to a deliberate constant cadence only when the workflow requires it. Select the rate from source measurements; dropping/duplicating frames changes motion and duration/timestamps:

```sh
ffmpeg -i "input.mkv" -map 0:v:0 -map 0:a? -vf "fps=30000/1001" -fps_mode cfr -c:v libx264 -preset medium -crf 20 -c:a copy "output-cfr.mkv"
```

For a strict low-latency derivative, test reduced B-frame reordering with the target decoder. `-bf 2` still permits B-frames; `-bf 0` disables them, potentially increasing bitrate or reducing compression efficiency:

```sh
ffmpeg -i "input.mkv" -map 0:v:0 -map 0:a? -c:v libx264 -preset medium -crf 20 -bf 2 -c:a copy "output-low-latency.mkv"
```

After any change, compare seeking latency, clip boundary accuracy, playback compatibility, audio/subtitle preservation, and visual quality on representative clients. A remux cannot improve codec decode complexity or shorten a long GOP; a transcode cannot restore missing source frames or guarantee indexes for every muxer.

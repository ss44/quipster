// Static heuristics and pure display helpers for the admin diagnostics view.
// Keeping the rule dictionaries here lets the view components stay focused on
// presentation and makes the heuristics straightforward to unit test.

import type {
  Benchmarks,
  DiskMetric,
  GaugeSnapshot,
  HardwareStatus,
  MediaOptimizationStats,
  Metric,
  SystemInfo,
} from '../api'

/** Metadata-only explanation for one backend optimization code. */
export interface OptimizationRule {
  title: string
  detail: string
  guidance: string
}

/** One rule with a positive file count from the latest bounded probe. */
export interface OptimizationFinding extends OptimizationRule {
  code: string
  count: number
}

/** A troubleshooting hint rendered above the environment panel. */
export interface DiagnosticHint {
  tone: 'bad' | 'warn'
  text: string
}

/** Metric series plotted as sparklines in the gauge row. */
export const HISTORY_KEYS = ['cpu', 'processCpu', 'ram', 'vram', 'gpu'] as const

/** One of the gauge metrics retained for sparkline history. */
export type HistoryKey = (typeof HISTORY_KEYS)[number]

/** Rolling sample history keyed by gauge metric. */
export type MetricHistory = Record<HistoryKey, number[]>

/** Maximum samples retained per metric (~2 minutes at the stream cadence). */
export const HISTORY_LIMIT = 120

/**
 * Business dictionary for the bounded media probes. The backend reports codes
 * only, so the human-readable explanation and FFmpeg guidance live with the
 * UI. Unknown codes are intentionally ignored by {@link optimizationFindings}.
 */
export const optimizationRules: Record<string, OptimizationRule> = {
  video_codec_compatibility: {
    title: 'Video codec compatibility',
    detail: 'The video codec is not H.264. This is informational: transcode only when target clients cannot decode the source.',
    guidance: 'For broad compatibility, create an H.264 derivative; expect a full decode/encode and validate quality.',
  },
  audio_codec_compatibility: {
    title: 'Audio codec compatibility',
    detail: 'The selected audio codec is not AAC. Some browsers and devices may require an AAC derivative.',
    guidance: 'Use -c:a aac -b:a 192k when compatibility matters; audio re-encoding is lossy.',
  },
  high_video_bitrate: {
    title: 'High video bitrate',
    detail: 'Estimated video or container bitrate is at least 40 Mb/s and may increase storage-read and clip costs.',
    guidance: 'If source I/O is the bottleneck, make a lower-bitrate derivative; compare quality and storage throughput first.',
  },
  suspected_variable_frame_rate: {
    title: 'Possible variable frame rate',
    detail: 'Average and nominal frame rates differ by more than 5%; confirm with frame timestamps before changing cadence.',
    guidance: 'If constant frame rate is required, re-encode with an explicit -vf fps=... chosen for the source.',
  },
  deep_b_frame_reordering: {
    title: 'Deep B-frame reordering',
    detail: 'FFprobe reports more than 4 reordered B-frames. This is only a latency concern for strict low-latency workflows.',
    guidance: 'For low-latency derivatives, test -bf 2 or -bf 0; GOP spacing and random-access points matter more for seeking.',
  },
}

/**
 * Maps the backend's optimization code counts to readable findings. Codes
 * with a zero count or no matching rule are omitted so the panel never shows
 * an empty recommendation.
 */
export function optimizationFindings(
  stats: MediaOptimizationStats | null | undefined,
): OptimizationFinding[] {
  return Object.entries(stats?.codes ?? {})
    .filter(([code, count]) => count > 0 && optimizationRules[code])
    .map(([code, count]) => ({ code, count, ...optimizationRules[code] }))
}

/** Creates an empty rolling metric history. */
export function createMetricHistory(): MetricHistory {
  return {
    cpu: [],
    processCpu: [],
    ram: [],
    vram: [],
    gpu: [],
  }
}

/**
 * Converts a gauge metric to the 0-100 scale used by the sparklines. Percent
 * metrics are already on that scale; byte metrics are normalized against
 * their total when one is reported.
 */
export function metricPercent(metric: Metric): number | null {
  if (!metric.available) return null
  if (metric.unit === 'percent') return clampPercent(metric.value)
  if (metric.total && metric.total > 0) return clampPercent((metric.value / metric.total) * 100)
  return 0
}

/**
 * Appends one sample to a metric history, dropping the oldest sample once the
 * window is full. Unavailable metrics leave the series untouched so a single
 * failed sample does not crash the sparkline to zero.
 */
export function appendHistorySample(
  history: MetricHistory,
  key: HistoryKey,
  metric: Metric,
  limit = HISTORY_LIMIT,
): void {
  const value = metricPercent(metric)
  if (value === null) return
  const samples = history[key]
  samples.push(value)
  if (samples.length > limit) samples.shift()
}

/**
 * Derives actionable troubleshooting hints from the latest hardware, gauge
 * and system snapshots. The order is intentional: the most severe condition
 * in a category is reported once rather than stacking related warnings.
 */
export function buildDiagnosticHints(
  hardware: HardwareStatus | null,
  gauge: GaugeSnapshot | null,
  system: SystemInfo | null,
): DiagnosticHint[] {
  const out: DiagnosticHint[] = []
  if (!hardware) return out

  const requestedForced = hardware.requested !== 'auto' && hardware.requested !== 'none'
  if (!hardware.available && requestedForced) {
    out.push({
      tone: 'bad',
      text: `HWACCEL=${hardware.requested} was requested but the probe failed. Check device passthrough and driver capabilities, or set HWACCEL=auto.`,
    })
  } else if (!hardware.available && (gauge?.cpu.value ?? 0) > 85) {
    out.push({
      tone: 'warn',
      text: 'Software encoding while the CPU is saturated. Pick a GPU compose profile (igpu/nvidia) to move encoding off the CPU.',
    })
  }

  if ((hardware.stats?.fallbacks ?? 0) > 0) {
    out.push({
      tone: 'warn',
      text: 'A hardware render failed and fell back to CPU. Inspect recent logs and update the GPU driver if this repeats.',
    })
  }

  const cache = gauge?.disks?.cache
  if (cache?.available && cache.totalBytes > 0 && cache.freeBytes / cache.totalBytes < 0.1) {
    out.push({
      tone: 'warn',
      text: 'The clip cache is nearly full; LRU eviction may start thrashing. Free space or raise CACHE_MAX_GB.',
    })
  }

  for (const path of system?.paths ?? []) {
    if (path.name !== 'media' && path.path.startsWith('/mnt/')) {
      out.push({
        tone: 'warn',
        text: `The ${path.name} directory is on a mounted path. Keep DATA_DIR and CACHE_DIR on local storage for best throughput.`,
      })
    }
  }

  if (hardware.available && hardware.backend === 'nvenc' && gauge && !gauge.gpu.available) {
    out.push({
      tone: 'warn',
      text: 'NVENC is active but nvidia-smi is unavailable in this container, so GPU utilization and VRAM cannot be shown.',
    })
  }

  return out
}

/** Percentage of a disk that is currently used, clamped to 0-100. */
export function diskUsedPct(disk: DiskMetric): number {
  if (!disk.available || disk.totalBytes <= 0) return 0
  return clampPercent(((disk.totalBytes - disk.freeBytes) / disk.totalBytes) * 100)
}

/** Compact job timing for the benchmark table (ms under one second). */
export function fmtMs(ms: number): string {
  if (!ms) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/** Human-readable process uptime, omitting units that are still zero. */
export function fmtUptime(sec: number): string {
  if (!sec) return '—'
  const days = Math.floor(sec / 86400)
  const hours = Math.floor((sec % 86400) / 3600)
  const minutes = Math.floor((sec % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

/** Clock label for the last streamed sample, or "waiting" before the first. */
export function updateClock(updatedAt: number): string {
  if (!updatedAt) return 'waiting'
  return new Date(updatedAt).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** Sorted, de-duplicated codec list shown in the environment panel. */
export function codecCapabilities(system: SystemInfo | null, limit = 18): string[] {
  const all = [...(system?.encoders ?? []), ...(system?.decoders ?? [])]
  return [...new Set(all)].sort().slice(0, limit)
}

/** Completed throughput headline, formatted to two decimal places. */
export function throughputLabel(bench: Benchmarks | null): string {
  return (bench?.throughputPerMin ?? 0).toFixed(2)
}

/** Source-read headline; returns a dash when the backend could not derive it. */
export function readThroughputLabel(bench: Benchmarks | null): string {
  return bench?.readAvailable ? bench.readMBps.toFixed(1) : 'n/a'
}

function clampPercent(value: number): number {
  return Math.min(100, Math.max(0, value))
}

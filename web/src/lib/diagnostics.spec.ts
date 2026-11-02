import { describe, expect, it } from 'vitest'
import type {
  DiskMetric,
  GaugeSnapshot,
  GPUSummary,
  HardwareStatus,
  Metric,
  PathInfo,
  SystemInfo,
  VersionInfo,
} from '../api'
import {
  appendHistorySample,
  buildDiagnosticHints,
  codecCapabilities,
  createMetricHistory,
  diskUsedPct,
  fmtMs,
  fmtUptime,
  metricPercent,
  optimizationFindings,
  readThroughputLabel,
  throughputLabel,
  updateClock,
} from './diagnostics'

function metric(value: number, extra: Partial<Metric> = {}): Metric {
  return { updatedAt: 0, value, available: true, ...extra }
}

function hardware(extra: Partial<HardwareStatus> = {}): HardwareStatus {
  return {
    requested: 'auto',
    backend: 'none',
    device: '',
    encoder: 'libx264',
    decoder: '',
    available: false,
    reason: '',
    stats: {
      lastFallbackNote: '',
      hwEncodes: 0,
      cpuRenders: 0,
      fallbacks: 0,
      lastFallbackAt: 0,
    },
    ...extra,
  }
}

function gpu(extra: Partial<GPUSummary> = {}): GPUSummary {
  return {
    name: '',
    driverVersion: '',
    memoryTotalBytes: 0,
    memoryUsedBytes: 0,
    utilizationPct: 0,
    available: false,
    ...extra,
  }
}

function version(extra: Partial<VersionInfo> = {}): VersionInfo {
  return { version: '', available: false, ...extra }
}

function system(extra: Partial<SystemInfo> = {}): SystemInfo {
  return {
    generatedAt: 0,
    config: {},
    hardware: hardware(),
    gpu: gpu(),
    osName: '',
    kernel: '',
    appVersion: '',
    goVersion: '',
    goarch: '',
    goos: '',
    cpuModel: '',
    ffmpeg: version(),
    ffprobe: version(),
    paths: [],
    hwaccels: [],
    encoders: [],
    decoders: [],
    cpuCores: 4,
    appUptimeSec: 0,
    dbBytes: 0,
    meiliUp: false,
    container: false,
    ...extra,
  }
}

function disk(extra: Partial<DiskMetric> = {}): DiskMetric {
  return {
    path: '/data',
    name: 'data',
    freeBytes: 0,
    totalBytes: 0,
    available: true,
    ...extra,
  }
}

function path(extra: Partial<PathInfo> = {}): PathInfo {
  return {
    name: 'data',
    path: '/data',
    freeBytes: 0,
    totalBytes: 0,
    available: true,
    ...extra,
  }
}

function gauge(extra: Partial<GaugeSnapshot> = {}): GaugeSnapshot {
  return {
    at: 0,
    cacheParts: {},
    disks: {},
    cpu: metric(5),
    processCpu: metric(5),
    ram: metric(5),
    swap: metric(0),
    load: { one: 0, five: 0, fifteen: 0, available: false },
    gpu: metric(0),
    vram: metric(0),
    hardware: hardware(),
    cacheBytes: 0,
    ...extra,
  }
}

describe('optimizationFindings', () => {
  it('maps positive known codes to their readable rule', () => {
    const findings = optimizationFindings({
      updatedAt: 0,
      scanned: 10,
      recommended: 2,
      codes: {
        video_codec_compatibility: 2,
        high_video_bitrate: 0,
        unknown_code: 5,
      },
    })

    expect(findings).toHaveLength(1)
    expect(findings[0]).toMatchObject({
      code: 'video_codec_compatibility',
      count: 2,
      title: 'Video codec compatibility',
    })
    expect(findings[0].guidance).toContain('H.264 derivative')
  })

  it('returns no findings when stats are missing', () => {
    expect(optimizationFindings(null)).toEqual([])
    expect(optimizationFindings(undefined)).toEqual([])
  })
})

describe('buildDiagnosticHints', () => {
  it('returns nothing until hardware has been probed', () => {
    expect(buildDiagnosticHints(null, gauge(), system())).toEqual([])
  })

  it('reports a forced hardware backend that failed to probe', () => {
    const hints = buildDiagnosticHints(
      hardware({ requested: 'nvenc', available: false }),
      gauge(),
      system(),
    )
    expect(hints).toHaveLength(1)
    expect(hints[0].tone).toBe('bad')
    expect(hints[0].text).toContain('HWACCEL=nvenc')
  })

  it('warns about saturated software encoding instead of a forced failure', () => {
    const hints = buildDiagnosticHints(
      hardware({ requested: 'auto', available: false }),
      gauge({ cpu: metric(90) }),
      system(),
    )
    expect(hints[0]).toMatchObject({ tone: 'warn' })
    expect(hints[0].text).toContain('CPU is saturated')
  })

  it('includes fallbacks, cache pressure, mounted paths and missing nvidia-smi', () => {
    const hints = buildDiagnosticHints(
      hardware({ available: true, backend: 'nvenc', stats: { ...hardware().stats, fallbacks: 2 } }),
      gauge({
        gpu: metric(0, { available: false }),
        disks: { cache: disk({ name: 'cache', totalBytes: 100, freeBytes: 5 }) },
      }),
      system({ paths: [path({ name: 'data', path: '/mnt/data' })] }),
    )
    const text = hints.map((hint) => hint.text).join(' ')
    expect(text).toContain('fell back to CPU')
    expect(text).toContain('clip cache is nearly full')
    expect(text).toContain('data directory is on a mounted path')
    expect(text).toContain('nvidia-smi is unavailable')
  })

  it('does not flag the media path or a healthy cache', () => {
    const hints = buildDiagnosticHints(
      hardware({ available: true, backend: 'qsv' }),
      gauge({ disks: { cache: disk({ name: 'cache', totalBytes: 100, freeBytes: 50 }) } }),
      system({ paths: [path({ name: 'media', path: '/mnt/media' })] }),
    )
    expect(hints).toEqual([])
  })
})

describe('metric history', () => {
  it('starts empty for every gauge series', () => {
    expect(createMetricHistory()).toEqual({
      cpu: [],
      processCpu: [],
      ram: [],
      vram: [],
      gpu: [],
    })
  })

  it('normalizes percent and byte metrics for the sparkline scale', () => {
    expect(metricPercent(metric(42, { unit: 'percent' }))).toBe(42)
    expect(metricPercent(metric(50, { total: 200, unit: 'bytes' }))).toBe(25)
    expect(metricPercent(metric(50, { unit: 'bytes' }))).toBe(0)
    expect(metricPercent(metric(140, { unit: 'percent' }))).toBe(100)
    expect(metricPercent(metric(-5, { unit: 'percent' }))).toBe(0)
    expect(metricPercent(metric(1, { available: false }))).toBeNull()
  })

  it('skips unavailable samples and keeps only the newest ones', () => {
    const history = createMetricHistory()
    appendHistorySample(history, 'cpu', metric(1, { unit: 'percent' }), 2)
    appendHistorySample(history, 'cpu', metric(2, { unit: 'percent', available: false }), 2)
    appendHistorySample(history, 'cpu', metric(3, { unit: 'percent' }), 2)
    appendHistorySample(history, 'cpu', metric(4, { unit: 'percent' }), 2)
    expect(history.cpu).toEqual([3, 4])
  })
})

describe('diagnostics formatting', () => {
  it('clamps disk usage to a percentage', () => {
    expect(diskUsedPct(disk({ totalBytes: 100, freeBytes: 25 }))).toBe(75)
    expect(diskUsedPct(disk({ totalBytes: 100, freeBytes: 200 }))).toBe(0)
    expect(diskUsedPct(disk({ totalBytes: 0, freeBytes: 0 }))).toBe(0)
    expect(diskUsedPct(disk({ available: false, totalBytes: 100, freeBytes: 0 }))).toBe(0)
  })

  it('formats benchmark timings', () => {
    expect(fmtMs(0)).toBe('—')
    expect(fmtMs(480)).toBe('480ms')
    expect(fmtMs(1500)).toBe('1.5s')
  })

  it('formats uptime with only the significant units', () => {
    expect(fmtUptime(0)).toBe('—')
    expect(fmtUptime(90)).toBe('1m')
    expect(fmtUptime(3660)).toBe('1h 1m')
    expect(fmtUptime(90000)).toBe('1d 1h')
  })

  it('labels the last update and throughput headlines', () => {
    expect(updateClock(0)).toBe('waiting')
    expect(updateClock(Date.UTC(2026, 0, 2, 3, 4, 5))).not.toBe('waiting')
    expect(throughputLabel(null)).toBe('0.00')
    expect(readThroughputLabel(null)).toBe('n/a')
    expect(
      readThroughputLabel({
        at: 0,
        window: '1h',
        since: 0,
        jobs: [],
        throughputPerMin: 1,
        readMBps: 12.34,
        readAvailable: true,
      }),
    ).toBe('12.3')
  })

  it('returns sorted, unique and bounded codec capabilities', () => {
    expect(codecCapabilities(null)).toEqual([])
    expect(
      codecCapabilities(
        system({ encoders: ['h264', 'hevc', 'h264'], decoders: ['aac', 'h264'] }),
      ),
    ).toEqual(['aac', 'h264', 'hevc'])
    expect(codecCapabilities(system({ encoders: ['b', 'a', 'c'] }), 2)).toEqual(['a', 'b'])
  })
})

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createVuetify } from 'vuetify'
import { VBtn, VCheckbox, VDialog, VIcon, VSelect } from 'vuetify/components'
import DiagnosticsView from '../DiagnosticsView.vue'
import LogsDialog from '../diagnostics/LogsDialog.vue'
import type {
  Benchmarks,
  DiskMetric,
  GaugeSnapshot,
  GPUSummary,
  HardwareStatus,
  MediaOptimizationStats,
  Metric,
  SystemInfo,
  VersionInfo,
} from '../../api'
import { api } from '../../api'

vi.mock('../../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api')>()
  return {
    ...actual,
    api: {
      diagnosticsSystem: vi.fn(),
      diagnosticsMetrics: vi.fn(),
      diagnosticsBenchmarks: vi.fn(),
      diagnosticsMediaOptimization: vi.fn(),
      diagnosticsProbe: vi.fn(),
      diagnosticsReport: vi.fn(),
      diagnosticsBundleURL: vi.fn(),
      logs: vi.fn(),
    },
  }
})

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
    name: 'Test GPU',
    driverVersion: '550.54',
    memoryTotalBytes: 0,
    memoryUsedBytes: 0,
    utilizationPct: 0,
    available: false,
    ...extra,
  }
}

function version(extra: Partial<VersionInfo> = {}): VersionInfo {
  return { version: '6.1', available: true, ...extra }
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

function gauge(extra: Partial<GaugeSnapshot> = {}): GaugeSnapshot {
  return {
    at: 0,
    cacheParts: { clips: 512 },
    disks: {
      data: disk({ name: 'data', path: '/data', totalBytes: 100, freeBytes: 25 }),
      cache: disk({ name: 'cache', path: '/cache', totalBytes: 100, freeBytes: 5 }),
    },
    cpu: metric(10, { unit: 'percent' }),
    processCpu: metric(5, { unit: 'percent' }),
    ram: metric(50, { total: 100, unit: 'bytes' }),
    swap: metric(0, { unit: 'percent' }),
    load: { one: 1.2, five: 0.5, fifteen: 0.25, available: true },
    gpu: metric(0, { available: false, reason: 'nvidia-smi missing' }),
    vram: metric(0, { available: false }),
    hardware: hardware(),
    cacheBytes: 1024,
    ...extra,
  }
}

function system(extra: Partial<SystemInfo> = {}): SystemInfo {
  return {
    generatedAt: 0,
    config: { HWACCEL: 'auto' },
    hardware: hardware(),
    gpu: gpu(),
    osName: 'Ubuntu 24.04',
    kernel: '6.8.0',
    appVersion: '1.2.3',
    goVersion: 'go1.23',
    goarch: 'amd64',
    goos: 'linux',
    cpuModel: 'Test CPU',
    ffmpeg: version({ version: '7.0.2' }),
    ffprobe: version({ version: '7.0.2' }),
    paths: [],
    hwaccels: ['vaapi'],
    encoders: ['libx264', 'h264_vaapi'],
    decoders: ['h264'],
    cpuCores: 8,
    appUptimeSec: 3660,
    dbBytes: 2048,
    meiliUp: true,
    container: true,
    ...extra,
  }
}

function benchmarks(): Benchmarks {
  return {
    at: 0,
    window: '1h',
    since: 0,
    jobs: [
      {
        type: 'probe',
        count: 3,
        p50Ms: 500,
        p95Ms: 1500,
        maxMs: 2000,
        avgWaitMs: 250,
      },
    ],
    throughputPerMin: 2.5,
    readMBps: 12.5,
    readAvailable: true,
  }
}

function optimization(): MediaOptimizationStats {
  return {
    updatedAt: 0,
    scanned: 10,
    recommended: 2,
    codes: { video_codec_compatibility: 2 },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function deferSnapshots() {
  const requests = {
    system: deferred<SystemInfo>(),
    metrics: deferred<GaugeSnapshot>(),
    bench: deferred<Benchmarks>(),
    optimization: deferred<MediaOptimizationStats>(),
  }
  vi.mocked(api.diagnosticsSystem).mockReturnValueOnce(requests.system.promise)
  vi.mocked(api.diagnosticsMetrics).mockReturnValueOnce(requests.metrics.promise)
  vi.mocked(api.diagnosticsBenchmarks).mockReturnValueOnce(requests.bench.promise)
  vi.mocked(api.diagnosticsMediaOptimization).mockReturnValueOnce(requests.optimization.promise)
  return requests
}

class FakeEventSource {
  static instances: FakeEventSource[] = []
  onerror: (() => void) | null = null
  closed = false
  private tick: ((event: MessageEvent) => void) | null = null

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void) {
    if (type === 'tick') this.tick = listener
  }

  close() {
    this.closed = true
  }

  emitTick(snapshot: GaugeSnapshot) {
    this.tick?.({ data: JSON.stringify(snapshot) } as MessageEvent)
  }
}

const vuetify = createVuetify({
  components: { VBtn, VCheckbox, VDialog, VIcon, VSelect },
})

let wrapper: VueWrapper | null = null

beforeEach(() => {
  FakeEventSource.instances = []
  vi.stubGlobal('EventSource', FakeEventSource)
  vi.mocked(api.diagnosticsSystem).mockResolvedValue(system())
  vi.mocked(api.diagnosticsMetrics).mockResolvedValue(gauge())
  vi.mocked(api.diagnosticsBenchmarks).mockResolvedValue(benchmarks())
  vi.mocked(api.diagnosticsMediaOptimization).mockResolvedValue(optimization())
  vi.mocked(api.logs).mockResolvedValue({ lines: ['line one'], count: 1 })
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

async function mountView() {
  wrapper = mount(DiagnosticsView, {
    attachTo: document.body,
    global: { plugins: [vuetify] },
  })
  await flushPromises()
}

describe('DiagnosticsView composition', () => {
  it('settles each initial source independently without early empty states or spinners', async () => {
    const requests = deferSnapshots()
    await mountView()

    for (const label of ['diagnostics status', 'sample time', 'hardware', 'CPU', 'storage', 'benchmarks', 'optimization findings', 'OS']) {
      expect(wrapper!.find(`[aria-label="Loading ${label}"]`).exists()).toBe(true)
    }
    expect(wrapper!.find('.v-progress-circular').exists()).toBe(false)
    expect(wrapper!.text()).not.toContain('Stream stale')
    expect(wrapper!.text()).not.toContain('No completed jobs')
    expect(wrapper!.text()).not.toContain('No probed media')
    expect(wrapper!.text()).not.toContain('n/a')

    requests.metrics.resolve(gauge())
    await flushPromises()
    expect(wrapper!.find('.gauges [aria-label="Loading CPU"]').exists()).toBe(false)
    expect(wrapper!.find('.head-status').text()).toContain('Live')
    expect(wrapper!.findAll('.gauge')[0].text()).toContain('10%')
    expect(wrapper!.find('[aria-label="Loading OS"]').exists()).toBe(true)
    expect(wrapper!.find('[aria-label="Loading benchmarks"]').exists()).toBe(true)

    requests.system.resolve(system())
    await flushPromises()
    expect(wrapper!.text()).toContain('Ubuntu 24.04')
    expect(wrapper!.find('[aria-label="Loading OS"]').exists()).toBe(false)
    expect(wrapper!.find('[aria-label="Loading optimization findings"]').exists()).toBe(true)

    requests.bench.resolve({ ...benchmarks(), jobs: [], throughputPerMin: 0 })
    await flushPromises()
    expect(wrapper!.text()).toContain('No completed jobs in this window.')
    expect(wrapper!.find('[aria-label="Loading benchmarks"]').exists()).toBe(false)
    expect(wrapper!.find('[aria-label="Loading optimization findings"]').exists()).toBe(true)

    requests.optimization.resolve({ updatedAt: 0, scanned: 0, recommended: 0, codes: {} })
    await flushPromises()
    expect(wrapper!.text()).toContain('No probed media findings yet.')
    expect(wrapper!.find('[role="status"]').exists()).toBe(false)
  })

  it('settles initial failures to unavailable and allows the metrics stream to recover', async () => {
    const requests = deferSnapshots()
    await mountView()

    requests.bench.reject(new Error('Benchmarks failed'))
    await flushPromises()
    expect(wrapper!.text()).toContain('Performance unavailable.')
    expect(wrapper!.find('[aria-label="Loading OS"]').exists()).toBe(true)

    requests.system.reject(new Error('System failed'))
    requests.metrics.reject(new Error('Metrics failed'))
    requests.optimization.reject(new Error('Optimization failed'))
    await flushPromises()
    expect(wrapper!.find('[role="status"]').exists()).toBe(false)
    expect(wrapper!.text()).toContain('Metrics unavailable')
    expect(wrapper!.text()).toContain('HARDWARE UNAVAILABLE')
    expect(wrapper!.text()).toContain('Storage unavailable.')
    expect(wrapper!.text()).toContain('Optimization checks unavailable.')
    expect(wrapper!.find('.env-panel').text()).not.toContain('offline')
    expect(wrapper!.find('.env-panel').text()).not.toContain('Host')
    expect(wrapper!.text()).not.toContain('0 B cached')
    expect(wrapper!.text()).not.toContain('0 of 0')
    expect(wrapper!.text()).not.toContain('No completed jobs')

    FakeEventSource.instances[0].emitTick(gauge())
    await flushPromises()
    expect(wrapper!.find('.head-status').text()).toContain('Live')
    expect(wrapper!.findAll('.gauge')[0].text()).toContain('10%')
    expect(wrapper!.text()).toContain('1.0 KB cached')
  })

  it('keeps successful sources when a different initial request fails', async () => {
    vi.mocked(api.diagnosticsBenchmarks).mockRejectedValueOnce(new Error('Benchmarks failed'))
    await mountView()
    expect(wrapper!.text()).toContain('Ubuntu 24.04')
    expect(wrapper!.text()).toContain('Video codec compatibility')
    expect(wrapper!.text()).toContain('Performance unavailable.')
    expect(wrapper!.find('[role="status"]').exists()).toBe(false)
  })

  it('preserves snapshots during refresh and after refresh failure', async () => {
    await mountView()
    const requests = deferSnapshots()
    await wrapper!.find('.head-status button').trigger('click')
    await flushPromises()

    expect(wrapper!.find('[role="status"]').exists()).toBe(false)
    expect(wrapper!.text()).toContain('Ubuntu 24.04')
    expect(wrapper!.text()).toContain('Video codec compatibility')
    expect(wrapper!.text()).toContain('2.50')
    expect(wrapper!.findAll('.gauge')[0].text()).toContain('10%')

    for (const request of Object.values(requests)) request.reject(new Error('Refresh failed'))
    await flushPromises()
    expect(wrapper!.text()).toContain('Refresh failed')
    expect(wrapper!.text()).toContain('Ubuntu 24.04')
    expect(wrapper!.text()).toContain('Video codec compatibility')
    expect(wrapper!.text()).toContain('2.50')
    expect(wrapper!.findAll('.gauge')[0].text()).toContain('10%')
    expect(wrapper!.find('[role="status"]').exists()).toBe(false)
  })

  it('distinguishes pending logs from a loaded empty response and preserves it on refresh', async () => {
    const first = deferred<{ lines: string[]; count: number }>()
    vi.mocked(api.logs).mockReturnValueOnce(first.promise)
    await mountView()
    await wrapper!.findAll('button').find((button) => button.text().includes('Open logs'))!.trigger('click')
    await flushPromises()

    expect(document.querySelector('[aria-label="Loading logs"]')).not.toBeNull()
    expect(document.querySelector('.log-sub')!.textContent).not.toContain('0 lines')
    expect(document.querySelector('.log-lines')!.textContent).not.toContain('No log lines')
    expect(document.querySelector('.log-panel .v-progress-circular')).toBeNull()

    first.resolve({ lines: [], count: 0 })
    await flushPromises()
    expect(document.querySelector('[aria-label="Loading logs"]')).toBeNull()
    expect(document.querySelector('.log-sub')!.textContent).toContain('0 lines')
    expect(document.querySelector('.log-lines')!.textContent).toContain('No log lines captured yet.')

    const refresh = deferred<{ lines: string[]; count: number }>()
    vi.mocked(api.logs).mockReturnValueOnce(refresh.promise)
    wrapper!.findComponent(LogsDialog).vm.$emit('refresh')
    await flushPromises()
    expect(document.querySelector('[aria-label="Loading logs"]')).toBeNull()
    expect(document.querySelector('.log-lines')!.textContent).toContain('No log lines captured yet.')
    refresh.resolve({ lines: ['New log entry'], count: 1 })
    await flushPromises()
    expect(document.querySelector('.log-lines')!.textContent).toContain('New log entry')
  })

  it('settles log failure without claiming emptiness and preserves recovered logs on refresh failure', async () => {
    const first = deferred<{ lines: string[]; count: number }>()
    vi.mocked(api.logs).mockReturnValueOnce(first.promise)
    await mountView()
    await wrapper!.findAll('button').find((button) => button.text().includes('Open logs'))!.trigger('click')
    await flushPromises()
    first.reject(new Error('Log request failed'))
    await flushPromises()

    expect(document.querySelector('[aria-label="Loading logs"]')).toBeNull()
    expect(document.querySelector('.log-lines')!.textContent).toContain('Logs unavailable.')
    expect(document.querySelector('.log-panel [role="alert"]')!.textContent).toContain('Log request failed')
    expect(document.querySelector('.log-sub')!.textContent).not.toContain('0 lines')

    wrapper!.findComponent(LogsDialog).vm.$emit('refresh')
    await flushPromises()
    expect(document.querySelector('.log-lines')!.textContent).toContain('line one')

    const refresh = deferred<{ lines: string[]; count: number }>()
    vi.mocked(api.logs).mockReturnValueOnce(refresh.promise)
    wrapper!.findComponent(LogsDialog).vm.$emit('refresh')
    await flushPromises()
    expect(document.querySelector('[aria-label="Loading logs"]')).toBeNull()
    expect(document.querySelector('.log-lines')!.textContent).toContain('line one')
    refresh.reject(new Error('Refresh failed'))
    await flushPromises()
    expect(document.querySelector('.log-lines')!.textContent).toContain('line one')
    expect(document.querySelector('.log-panel [role="alert"]')!.textContent).toContain('Refresh failed')
  })

  it('renders the focused panels from the loaded diagnostics snapshots', async () => {
    await mountView()

    const text = wrapper!.text()
    expect(text).toContain('Hardware acceleration')
    expect(text).toContain('driver 550.54')
    expect(text).toContain('SOFTWARE · CPU')
    expect(text).toContain('Storage & cache')
    expect(text).toContain('data')
    expect(text).toContain('1.0 KB cached')
    expect(text).toContain('Performance')
    expect(text).toContain('2.50')
    expect(text).toContain('12.5')
    expect(text).toContain('Media optimization checks')
    expect(text).toContain('Video codec compatibility')
    expect(text).toContain('Environment')
    expect(text).toContain('Ubuntu 24.04')
    expect(text).toContain('h264_vaapi')
    expect(text).toContain('HWACCEL=auto')
    expect(text).toContain('clip cache is nearly full')
  })

  it('applies streamed gauge samples and builds sparkline history', async () => {
    await mountView()

    expect(FakeEventSource.instances).toHaveLength(1)
    expect(FakeEventSource.instances[0].url).toBe('/api/admin/diagnostics/stream')
    expect(wrapper!.findAll('.gauge-spark')).toHaveLength(0)

    const next = gauge({
      cpu: metric(55, { unit: 'percent' }),
      processCpu: metric(25, { unit: 'percent' }),
    })
    FakeEventSource.instances[0].emitTick(next)
    await flushPromises()

    const cpuGauge = wrapper!.findAll('.gauge')[0]
    expect(cpuGauge.text()).toContain('55%')
    expect(wrapper!.findAll('.gauge-spark').length).toBeGreaterThan(0)
  })

  it('closes the stream when the view unmounts', async () => {
    await mountView()
    const source = FakeEventSource.instances[0]

    wrapper!.unmount()
    wrapper = null

    expect(source.closed).toBe(true)
  })
})

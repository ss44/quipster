<template>
  <div class="page">
    <header class="page-head">
      <div>
        <p class="eyebrow">Admin</p>
        <h1 class="page-title">Diagnostics</h1>
        <p class="page-sub">Hardware, resources and performance — without digging through log files.</p>
      </div>
      <div class="head-status">
        <span class="status-dot" :class="stale ? 'err' : 'ok'"></span>
        <span class="status-text">{{ stale ? 'Stream stale' : 'Live' }}</span>
        <span class="divider-y"></span>
        <span class="status-text">{{ lastUpdateLabel }}</span>
        <v-btn size="small" variant="text" :loading="loading" @click="refreshAll">Refresh</v-btn>
      </div>
    </header>

    <p v-if="error" class="error-msg">{{ error }}</p>

    <section class="panel hw-panel">
      <header class="panel-header">
        <span>Hardware acceleration</span>
        <span class="panel-hint">{{ system?.gpu?.driverVersion ? `driver ${system.gpu.driverVersion}` : 'active pipeline' }}</span>
      </header>
      <div class="panel-body hw-body">
        <HardwareBadge :hardware="hardware" :gpu="gpu" />
        <div class="hw-actions">
          <v-btn size="small" color="primary" variant="flat" :loading="exporting" @click="exportBundle">
            <v-icon icon="mdi-download" size="16" class="mr-1" /> Export bundle
          </v-btn>
          <v-btn size="small" variant="tonal" :loading="copying" @click="copyReport">
            <v-icon icon="mdi-content-copy" size="16" class="mr-1" /> Copy report
          </v-btn>
          <v-btn size="small" variant="text" @click="openLogs">
            <v-icon icon="mdi-text-box-outline" size="16" class="mr-1" /> Open logs
          </v-btn>
          <v-btn size="small" variant="text" :loading="probing" @click="reprobe">
            <v-icon icon="mdi-refresh" size="16" class="mr-1" /> Re-run probe
          </v-btn>
          <v-checkbox v-model="includePaths" density="compact" hide-details label="Include media paths in bundle" class="path-toggle" />
        </div>
      </div>
    </section>

    <section class="gauges">
      <MetricGauge label="CPU" :metric="gauge?.cpu" :samples="history.cpu" :max="100" />
      <MetricGauge label="Process CPU" :metric="gauge?.processCpu" :samples="history.processCpu" :max="cpuMax" />
      <MetricGauge label="RAM" :metric="gauge?.ram" :samples="history.ram" :max="100" />
      <MetricGauge label="VRAM" :metric="gauge?.vram" :samples="history.vram" :max="100" />
      <MetricGauge label="GPU" :metric="gauge?.gpu" :samples="history.gpu" :max="100" />
      <MetricGauge label="Swap" :metric="gauge?.swap" />
    </section>

    <div class="two-col">
      <section class="panel">
        <header class="panel-header">
          <span>Storage &amp; cache</span>
          <span class="panel-hint">{{ fmtBytes(gauge?.cacheBytes ?? 0) }} cached</span>
        </header>
        <div class="panel-body">
          <div v-for="d in disks" :key="d.name" class="disk-row">
            <div class="disk-top">
              <span class="disk-name">{{ d.name }}</span>
              <span class="mono">{{ d.available ? `${fmtBytes(d.freeBytes)} free` : d.reason || 'unavailable' }}</span>
            </div>
            <div class="disk-path mono truncate">{{ d.path || '—' }}</div>
            <div class="bar"><div class="fill" :style="{ width: `${usedPct(d)}%` }"></div></div>
          </div>
          <div v-if="!disks.length" class="idle">Waiting for storage sample…</div>

          <div class="cache-row">
            <span class="cache-label">Cache breakdown</span>
            <div class="chips">
              <span v-for="(bytes, kind) in gauge?.cacheParts ?? {}" :key="kind" class="mini-chip mono">
                {{ kind }} {{ fmtBytes(bytes) }}
              </span>
            </div>
          </div>
          <div class="load-row mono">
            load {{ gauge?.load?.available ? `${gauge.load.one.toFixed(2)} ${gauge.load.five.toFixed(2)} ${gauge.load.fifteen.toFixed(2)}` : 'n/a' }}
          </div>
        </div>
      </section>

      <section class="panel">
        <header class="panel-header">
          <span>Performance</span>
          <v-select
            v-model="windowSel"
            :items="windows"
            density="compact"
            variant="outlined"
            hide-details
            class="window-select"
            @update:model-value="loadBench"
          />
        </header>
        <div class="panel-body">
          <div class="perf-top">
            <div>
              <p class="perf-value">{{ (bench?.throughputPerMin ?? 0).toFixed(2) }}<span class="perf-unit">jobs/min</span></p>
              <p class="perf-sub">completed throughput</p>
            </div>
            <div>
              <p class="perf-value">{{ bench?.readAvailable ? `${bench.readMBps.toFixed(1)}` : 'n/a' }}<span v-if="bench?.readAvailable" class="perf-unit">MB/s</span></p>
              <p class="perf-sub">{{ bench?.readAvailable ? 'derived source read' : bench?.readReason || 'source read' }}</p>
            </div>
          </div>

          <div class="table-wrap">
            <table class="bench-table">
              <thead>
                <tr>
                  <th>Job</th>
                  <th>Count</th>
                  <th>p50</th>
                  <th>p95</th>
                  <th>Max</th>
                  <th>Wait</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="j in bench?.jobs ?? []" :key="j.type">
                  <td class="mono">{{ j.type }}</td>
                  <td class="mono">{{ j.count }}</td>
                  <td class="mono">{{ fmtMs(j.p50Ms) }}</td>
                  <td class="mono">{{ fmtMs(j.p95Ms) }}</td>
                  <td class="mono">{{ fmtMs(j.maxMs) }}</td>
                  <td class="mono">{{ fmtMs(j.avgWaitMs) }}</td>
                </tr>
              </tbody>
            </table>
            <p v-if="!bench?.jobs?.length" class="idle">No completed jobs in this window.</p>
          </div>
        </div>
      </section>
    </div>

    <section class="panel optimization-panel">
      <header class="panel-header">
        <span>Media optimization checks</span>
        <span class="panel-hint">{{ optimization?.recommended ?? 0 }} of {{ optimization?.scanned ?? 0 }} probed files flagged</span>
      </header>
      <div class="panel-body">
        <p class="optimization-note">Metadata-only heuristics; Quipster never modifies source media. Counts refresh with this view after media is reprobed.</p>
        <div v-if="optimizationFindings.length" class="optimization-list">
          <div v-for="finding in optimizationFindings" :key="finding.code" class="optimization-item">
            <div class="optimization-top"><strong>{{ finding.title }}</strong><span class="mini-chip mono">{{ finding.count }} files</span></div>
            <p>{{ finding.detail }}</p>
            <code>{{ finding.guidance }}</code>
          </div>
        </div>
        <p v-else class="idle">{{ optimization?.scanned ? 'No optimization recommendations recorded.' : 'No probed media findings yet.' }}</p>
        <p class="optimization-note">GOP/keyframe spacing and MP4 moov placement require packet/atom inspection and are not inferred from these bounded probes. See the media diagnostics reference for targeted FFprobe commands.</p>
      </div>
    </section>

    <section class="panel env-panel">
      <header class="panel-header">
        <span>Environment</span>
        <span class="panel-hint">ffmpeg {{ system?.ffmpeg?.version || 'unknown' }}</span>
      </header>
      <div class="panel-body">
        <div class="env-grid">
          <div class="env-item"><span class="env-label">OS</span><span>{{ system?.osName || '—' }}</span></div>
          <div class="env-item"><span class="env-label">Kernel</span><span class="mono">{{ system?.kernel || '—' }}</span></div>
          <div class="env-item"><span class="env-label">Runtime</span><span>{{ system?.container ? 'Docker' : 'Host' }} · {{ system?.goos }}/{{ system?.goarch }}</span></div>
          <div class="env-item"><span class="env-label">CPU</span><span>{{ system?.cpuModel || '—' }} ({{ system?.cpuCores ?? '—' }} cores)</span></div>
          <div class="env-item"><span class="env-label">Quipster</span><span>{{ system?.appVersion || 'dev' }} · {{ system?.goVersion || '—' }}</span></div>
          <div class="env-item"><span class="env-label">Uptime</span><span>{{ fmtUptime(system?.appUptimeSec ?? 0) }}</span></div>
          <div class="env-item"><span class="env-label">GPU</span><span>{{ gpu?.available ? gpu.name : gpu?.reason || 'not reported' }}</span></div>
          <div class="env-item"><span class="env-label">Driver</span><span class="mono">{{ gpu?.driverVersion || '—' }}</span></div>
          <div class="env-item"><span class="env-label">Search</span><span>{{ system?.meiliUp ? 'online' : 'offline' }}</span></div>
          <div class="env-item"><span class="env-label">Database</span><span>{{ fmtBytes(system?.dbBytes ?? 0) }}</span></div>
          <div class="env-item"><span class="env-label">ffprobe</span><span class="mono">{{ system?.ffprobe?.version || 'unavailable' }}</span></div>
          <div class="env-item"><span class="env-label">Hardware accel</span><span class="mono">{{ (system?.hwaccels ?? []).join(', ') || 'none' }}</span></div>
        </div>

        <div class="codec-block">
          <p class="env-label">Codec capabilities</p>
          <div class="chips">
            <span v-for="c in codecs" :key="c" class="mini-chip mono">{{ c }}</span>
            <span v-if="!codecs.length" class="faint">No codec details reported.</span>
          </div>
        </div>

        <div class="codec-block">
          <p class="env-label">Effective settings</p>
          <div class="chips">
            <span v-for="(value, key) in system?.config ?? {}" :key="key" class="mini-chip mono">
              {{ key }}={{ value }}
            </span>
          </div>
        </div>
      </div>
    </section>

    <section v-if="hints.length" class="panel hints-panel">
      <header class="panel-header"><span>Troubleshooting hints</span></header>
      <div class="panel-body hints">
        <div v-for="(hint, i) in hints" :key="i" class="hint" :class="hint.tone">
          <v-icon :icon="hint.tone === 'bad' ? 'mdi-alert' : 'mdi-information-outline'" size="16" class="mr-2" />
          <span>{{ hint.text }}</span>
        </div>
      </div>
    </section>

    <v-dialog v-model="logOpen" max-width="1000" scrollable>
      <div class="log-panel">
        <header class="log-head">
          <div>
            <h2 class="log-title">Recent logs</h2>
            <p class="log-sub">{{ logs.length }} lines from the API process</p>
          </div>
          <div class="log-actions">
            <v-btn size="small" variant="text" :loading="logLoading" @click="loadLogs">Refresh</v-btn>
            <v-btn size="small" variant="tonal" @click="copyLogs">Copy</v-btn>
            <v-btn size="small" variant="text" @click="logOpen = false">Close</v-btn>
          </div>
        </header>
        <pre class="log-lines">{{ logs.join('\n') || 'No log lines captured yet.' }}</pre>
      </div>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  api,
  diagnosticsStreamURL,
  type Benchmarks,
  type DiskMetric,
  type GaugeSnapshot,
  type GPUSummary,
  type HardwareStatus,
  type Metric,
  type MediaOptimizationStats,
  type SystemInfo,
} from '../api'
import HardwareBadge from './diagnostics/HardwareBadge.vue'
import MetricGauge from './diagnostics/MetricGauge.vue'

const system = ref<SystemInfo | null>(null)
const gauge = ref<GaugeSnapshot | null>(null)
const bench = ref<Benchmarks | null>(null)
const optimization = ref<MediaOptimizationStats | null>(null)
const loading = ref(true)
const error = ref('')
const stale = ref(true)
const lastUpdate = ref(0)
const windowSel = ref('1h')
const includePaths = ref(false)
const probing = ref(false)
const exporting = ref(false)
const copying = ref(false)
const logOpen = ref(false)
const logLoading = ref(false)
const logs = ref<string[]>([])

const windows = ['1h', '6h', '24h', '7d']
const history = reactive<Record<'cpu' | 'processCpu' | 'ram' | 'vram' | 'gpu', number[]>>({
  cpu: [],
  processCpu: [],
  ram: [],
  vram: [],
  gpu: [],
})

let stream: EventSource | null = null

const hardware = computed<HardwareStatus | null>(() => gauge.value?.hardware ?? system.value?.hardware ?? null)
const gpu = computed<GPUSummary | null>(() => system.value?.gpu ?? null)
const cpuMax = computed(() => Math.max(100, (system.value?.cpuCores ?? 1) * 100))
const disks = computed<DiskMetric[]>(() => Object.values(gauge.value?.disks ?? {}))
const codecs = computed(() => {
  const all = [...(system.value?.encoders ?? []), ...(system.value?.decoders ?? [])]
  return [...new Set(all)].sort().slice(0, 18)
})
const optimizationRules: Record<string, { title: string; detail: string; guidance: string }> = {
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
const optimizationFindings = computed(() => Object.entries(optimization.value?.codes ?? {})
  .filter(([code, count]) => count > 0 && optimizationRules[code])
  .map(([code, count]) => ({ code, count, ...optimizationRules[code] })))

const lastUpdateLabel = computed(() => {
  if (!lastUpdate.value) return 'waiting'
  return new Date(lastUpdate.value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
})

const hints = computed(() => {
  const out: { tone: string; text: string }[] = []
  const hw = hardware.value
  const g = gauge.value
  if (!hw) return out
  const requestedForced = hw.requested !== 'auto' && hw.requested !== 'none'
  if (!hw.available && requestedForced) {
    out.push({
      tone: 'bad',
      text: `HWACCEL=${hw.requested} was requested but the probe failed. Check device passthrough and driver capabilities, or set HWACCEL=auto.`,
    })
  } else if (!hw.available && (g?.cpu.value ?? 0) > 85) {
    out.push({
      tone: 'warn',
      text: 'Software encoding while the CPU is saturated. Pick a GPU compose profile (igpu/nvidia) to move encoding off the CPU.',
    })
  }
  if ((hw.stats?.fallbacks ?? 0) > 0) {
    out.push({
      tone: 'warn',
      text: 'A hardware render failed and fell back to CPU. Inspect recent logs and update the GPU driver if this repeats.',
    })
  }
  const cache = g?.disks?.cache
  if (cache?.available && cache.totalBytes > 0 && cache.freeBytes / cache.totalBytes < 0.1) {
    out.push({ tone: 'warn', text: 'The clip cache is nearly full; LRU eviction may start thrashing. Free space or raise CACHE_MAX_GB.' })
  }
  for (const p of system.value?.paths ?? []) {
    if (p.name !== 'media' && p.path.startsWith('/mnt/')) {
      out.push({ tone: 'warn', text: `The ${p.name} directory is on a mounted path. Keep DATA_DIR and CACHE_DIR on local storage for best throughput.` })
    }
  }
  if (hw.available && hw.backend === 'nvenc' && g && !g.gpu.available) {
    out.push({ tone: 'warn', text: 'NVENC is active but nvidia-smi is unavailable in this container, so GPU utilization and VRAM cannot be shown.' })
  }
  return out
})

function toast(text: string, color = 'success') {
  window.dispatchEvent(new CustomEvent('app-toast', { detail: { text, color } }))
}

function applyGauge(snap: GaugeSnapshot) {
  gauge.value = snap
  stale.value = false
  lastUpdate.value = Date.now()
  push('cpu', snap.cpu)
  push('processCpu', snap.processCpu)
  push('ram', snap.ram)
  push('vram', snap.vram)
  push('gpu', snap.gpu)
}

function push(key: keyof typeof history, m: Metric) {
  if (!m.available) return
  const value = m.unit === 'percent' ? m.value : m.total && m.total > 0 ? (m.value / m.total) * 100 : 0
  const arr = history[key]
  arr.push(value)
  if (arr.length > 120) arr.shift()
}

function connectStream() {
  if (stream) return
  stream = new EventSource(diagnosticsStreamURL)
  stream.addEventListener('tick', (event) => {
    try {
      applyGauge(JSON.parse((event as MessageEvent).data) as GaugeSnapshot)
    } catch {
      /* ignore malformed frames */
    }
  })
  stream.onerror = () => {
    stale.value = true
  }
}

function closeStream() {
  stream?.close()
  stream = null
  stale.value = true
}

function onVisibility() {
  if (document.hidden) {
    closeStream()
  } else {
    void refreshGauge()
    connectStream()
  }
}

async function bootstrap() {
  loading.value = true
  error.value = ''
  try {
    const [sys, b, mediaOptimization] = await Promise.all([
      api.diagnosticsSystem(),
      api.diagnosticsBenchmarks(windowSel.value),
      api.diagnosticsMediaOptimization(),
    ])
    system.value = sys
    bench.value = b
    optimization.value = mediaOptimization
  } catch (err) {
    error.value = (err as Error).message
  }
  await refreshGauge()
  connectStream()
  loading.value = false
}

async function refreshGauge() {
  try {
    applyGauge(await api.diagnosticsMetrics())
  } catch {
    /* the SSE stream may still recover */
  }
}

async function refreshAll() {
  loading.value = true
  try {
    const [sys, b, mediaOptimization] = await Promise.all([
      api.diagnosticsSystem(),
      api.diagnosticsBenchmarks(windowSel.value),
      api.diagnosticsMediaOptimization(),
    ])
    system.value = sys
    bench.value = b
    optimization.value = mediaOptimization
  } catch (err) {
    error.value = (err as Error).message
  }
  await refreshGauge()
  loading.value = false
}

async function loadBench() {
  try {
    bench.value = await api.diagnosticsBenchmarks(windowSel.value)
  } catch (err) {
    toast((err as Error).message, 'error')
  }
}

async function reprobe() {
  probing.value = true
  try {
    await api.diagnosticsProbe()
    await refreshAll()
    toast('Hardware probe refreshed')
  } catch (err) {
    toast((err as Error).message, 'error')
  } finally {
    probing.value = false
  }
}

function exportBundle() {
  exporting.value = true
  const a = document.createElement('a')
  a.href = api.diagnosticsBundleURL(includePaths.value)
  a.download = ''
  document.body.appendChild(a)
  a.click()
  a.remove()
  window.setTimeout(() => {
    exporting.value = false
    toast('Diagnostic bundle downloaded')
  }, 1200)
}

async function copyReport() {
  copying.value = true
  try {
    const text = await api.diagnosticsReport()
    await copyText(text)
    toast('Diagnostics report copied')
  } catch (err) {
    toast((err as Error).message, 'error')
  } finally {
    copying.value = false
  }
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
}

async function openLogs() {
  logOpen.value = true
  await loadLogs()
}

async function loadLogs() {
  logLoading.value = true
  try {
    const res = await api.logs(1000)
    logs.value = res.lines
  } catch (err) {
    toast((err as Error).message, 'error')
  } finally {
    logLoading.value = false
  }
}

async function copyLogs() {
  await copyText(logs.value.join('\n'))
  toast('Log lines copied')
}

function usedPct(d: DiskMetric): number {
  if (!d.available || d.totalBytes <= 0) return 0
  return Math.min(100, Math.max(0, ((d.totalBytes - d.freeBytes) / d.totalBytes) * 100))
}

function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  if (n < 1024) return `${Math.round(n)} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

function fmtMs(ms: number): string {
  if (!ms) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function fmtUptime(sec: number): string {
  if (!sec) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}


onMounted(() => {
  void bootstrap()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  closeStream()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<style scoped>
.page {
  max-width: 1520px;
  margin: 0 auto;
  padding: 24px 24px 64px;
}
.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 18px;
}
.page-title {
  margin: 4px 0 4px;
}
.head-status {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--muted);
}
.status-text {
  font-family: var(--font-mono);
  font-size: 11px;
}
.error-msg {
  margin: 0 0 14px;
  padding: 8px 12px;
  border: 1px solid rgba(248, 113, 113, 0.35);
  background: rgba(248, 113, 113, 0.08);
  border-radius: 8px;
  font-size: 12.5px;
  color: #fca5a5;
}
.hw-panel {
  margin-bottom: 12px;
}
.hw-body {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  flex-wrap: wrap;
}
.hw-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.path-toggle {
  font-size: 11.5px;
  max-width: 220px;
}
.gauges {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
@media (max-width: 1200px) {
  .gauges {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
@media (max-width: 640px) {
  .gauges {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
.two-col {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.25fr);
  gap: 12px;
  margin-bottom: 12px;
}
@media (max-width: 1000px) {
  .two-col {
    grid-template-columns: 1fr;
  }
}
.disk-row {
  padding: 7px 0 9px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}
.disk-row:first-child {
  padding-top: 0;
}
.disk-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 12px;
  color: var(--text);
}
.disk-name {
  text-transform: capitalize;
  font-weight: 550;
}
.disk-path {
  font-size: 10.5px;
  color: var(--faint);
  margin: 2px 0 6px;
}
.bar {
  height: 3px;
  border-radius: 2px;
  background: rgba(255, 255, 255, 0.07);
  overflow: hidden;
}
.bar .fill {
  height: 100%;
  background: var(--accent);
  border-radius: 2px;
}
.cache-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding-top: 12px;
}
.cache-label {
  font-size: 11px;
  color: var(--faint);
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}
.mini-chip {
  display: inline-block;
  padding: 2px 7px;
  border: 1px solid var(--border);
  border-radius: 5px;
  font-size: 10.5px;
  color: var(--muted);
  background: rgba(255, 255, 255, 0.03);
}
.load-row {
  margin-top: 10px;
  font-size: 11px;
  color: var(--faint);
}
.window-select {
  max-width: 92px;
  margin-top: -6px;
}
.perf-top {
  display: flex;
  gap: 32px;
  margin-bottom: 12px;
}
.perf-value {
  margin: 0;
  font-size: 22px;
  font-weight: 650;
  letter-spacing: -0.02em;
}
.perf-unit {
  margin-left: 5px;
  font-size: 11px;
  font-weight: 500;
  color: var(--faint);
}
.perf-sub {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--faint);
}
.table-wrap {
  overflow-x: auto;
}
.bench-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.bench-table th {
  text-align: left;
  padding: 7px 10px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--faint);
  border-bottom: 1px solid var(--border);
}
.bench-table td {
  padding: 7px 10px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.045);
}
.bench-table tr:last-child td {
  border-bottom: none;
}
.optimization-panel {
  margin-bottom: 12px;
}
.optimization-note {
  margin: 0 0 10px;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--faint);
}
.optimization-list {
  display: grid;
  gap: 10px;
}
.optimization-item {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.02);
}
.optimization-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 12px;
}
.optimization-item p {
  margin: 6px 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--muted);
}
.optimization-item code {
  display: block;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--text);
}
.env-panel {
  margin-bottom: 12px;
}
.env-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px 20px;
}
@media (max-width: 900px) {
  .env-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .env-grid {
    grid-template-columns: 1fr;
  }
}
.env-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
  color: var(--text);
  min-width: 0;
  overflow-wrap: anywhere;
}
.env-label {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--faint);
  margin: 0 0 4px;
}
.codec-block {
  margin-top: 16px;
}
.hints-panel {
  border-color: rgba(251, 191, 36, 0.3);
}
.hints {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.hint {
  display: flex;
  align-items: flex-start;
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--text);
}
.hint.bad {
  color: #fca5a5;
}
.hint.warn {
  color: #fcd34d;
}
.idle {
  margin: 8px 0;
  font-size: 12px;
  color: var(--faint);
}
.log-panel {
  display: flex;
  flex-direction: column;
  max-height: 80vh;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 14px;
  overflow: hidden;
}
.log-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.log-title {
  margin: 0;
  font-size: 16px;
  font-weight: 650;
}
.log-sub {
  margin: 2px 0 0;
  font-size: 11.5px;
  color: var(--faint);
}
.log-actions {
  display: flex;
  gap: 6px;
}
.log-lines {
  margin: 0;
  padding: 14px 16px;
  overflow: auto;
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  color: var(--muted);
  background: #0b0d10;
}
</style>

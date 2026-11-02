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

    <HardwarePanel
      v-model:include-paths="includePaths"
      :hardware="hardware"
      :gpu="gpu"
      :system="system"
      :exporting="exporting"
      :copying="copying"
      :probing="probing"
      @export="exportBundle"
      @copy-report="copyReport"
      @open-logs="openLogs"
      @reprobe="reprobe"
    />

    <MetricsPanel :gauge="gauge" :history="history" :cpu-max="cpuMax" />

    <div class="two-col">
      <CachePanel :gauge="gauge" />
      <PerformancePanel v-model:window="windowSel" :bench="bench" @change="loadBench" />
    </div>

    <OptimizationPanel :optimization="optimization" />
    <EnvironmentPanel :system="system" :gpu="gpu" />
    <HintsPanel v-if="hints.length" :hints="hints" />

    <LogsDialog
      v-model:open="logOpen"
      :logs="logs"
      :loading="logLoading"
      @refresh="loadLogs"
      @copy="copyLogs"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  api,
  diagnosticsStreamURL,
  type Benchmarks,
  type GaugeSnapshot,
  type GPUSummary,
  type HardwareStatus,
  type MediaOptimizationStats,
  type SystemInfo,
} from '../api'
import {
  HISTORY_KEYS,
  appendHistorySample,
  buildDiagnosticHints,
  createMetricHistory,
  updateClock,
  type MetricHistory,
} from '../lib/diagnostics'
import CachePanel from './diagnostics/CachePanel.vue'
import EnvironmentPanel from './diagnostics/EnvironmentPanel.vue'
import HardwarePanel from './diagnostics/HardwarePanel.vue'
import HintsPanel from './diagnostics/HintsPanel.vue'
import LogsDialog from './diagnostics/LogsDialog.vue'
import MetricsPanel from './diagnostics/MetricsPanel.vue'
import OptimizationPanel from './diagnostics/OptimizationPanel.vue'
import PerformancePanel from './diagnostics/PerformancePanel.vue'

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
const history = reactive<MetricHistory>(createMetricHistory())

let stream: EventSource | null = null

const hardware = computed<HardwareStatus | null>(
  () => gauge.value?.hardware ?? system.value?.hardware ?? null,
)
const gpu = computed<GPUSummary | null>(() => system.value?.gpu ?? null)
const cpuMax = computed(() => Math.max(100, (system.value?.cpuCores ?? 1) * 100))
const lastUpdateLabel = computed(() => updateClock(lastUpdate.value))
const hints = computed(() => buildDiagnosticHints(hardware.value, gauge.value, system.value))

function toast(text: string, color = 'success') {
  window.dispatchEvent(new CustomEvent('app-toast', { detail: { text, color } }))
}

function applyGauge(snap: GaugeSnapshot) {
  gauge.value = snap
  stale.value = false
  lastUpdate.value = Date.now()
  for (const key of HISTORY_KEYS) appendHistorySample(history, key, snap[key])
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
    const [sys, benchmarks, mediaOptimization] = await Promise.all([
      api.diagnosticsSystem(),
      api.diagnosticsBenchmarks(windowSel.value),
      api.diagnosticsMediaOptimization(),
    ])
    system.value = sys
    bench.value = benchmarks
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
    const [sys, benchmarks, mediaOptimization] = await Promise.all([
      api.diagnosticsSystem(),
      api.diagnosticsBenchmarks(windowSel.value),
      api.diagnosticsMediaOptimization(),
    ])
    system.value = sys
    bench.value = benchmarks
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
  const link = document.createElement('a')
  link.href = api.diagnosticsBundleURL(includePaths.value)
  link.download = ''
  document.body.appendChild(link)
  link.click()
  link.remove()
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
    const textarea = document.createElement('textarea')
    textarea.value = text
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    document.execCommand('copy')
    textarea.remove()
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
</style>

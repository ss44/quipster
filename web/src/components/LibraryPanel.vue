<template>
  <div class="page">
    <header class="page-head">
      <div>
        <p class="eyebrow">Admin</p>
        <h1 class="page-title">Library</h1>
        <p class="page-sub">Folders, ingestion and processing status.</p>
      </div>
      <div class="head-status">
        <span class="status-dot" :class="running.length ? 'run' : 'ok'"></span>
        <span class="status-text">{{ running.length ? `${running.length} processing` : 'Idle' }}</span>
        <span class="divider-y"></span>
        <span class="status-dot" :class="snap?.meiliUp ? 'ok' : 'err'"></span>
        <span class="status-text">Search {{ snap?.meiliUp ? 'online' : 'offline' }}</span>
      </div>
    </header>

    <section class="stats">
      <StatCard label="Library" :value="total.toLocaleString()" unit="files" :progress="libraryProgress">
        {{ n('indexed').toLocaleString() }} searchable · {{ n('pending').toLocaleString() }} queued ·
        {{ n('no_subs').toLocaleString() }} without subs
        <span v-if="n('error')" class="stat-error">· {{ n('error') }} failed</span>
      </StatCard>
      <StatCard
        label="Dialogue"
        :value="(snap?.lines?.cues ?? 0).toLocaleString()"
        unit="lines"
        :progress="dialogue.progress"
      >
        <span :class="{ 'stat-note': dialogue.state === 'keeping-up' }">{{ dialogue.sub }}</span>
      </StatCard>
      <StatCard
        label="Queue"
        :value="(snap?.queue?.queued ?? 0).toLocaleString()"
        unit="waiting"
        :progress="queueProgress"
      >
        <span v-if="paused" class="paused-tag">Paused</span>
        <template v-else>
          {{ snap?.queue?.running ?? 0 }} running · {{ snap?.queue?.done ?? 0 }} done
          <span v-if="snap?.queue?.error" class="stat-error">· {{ snap.queue.error }} failed</span>
        </template>
      </StatCard>
      <StatCard label="Clip cache" :value="fmtBytes(snap?.cacheBytes ?? 0)" :progress="cacheProgress">
        Clips, thumbnails and extracted subtitles, LRU-evicted.
      </StatCard>
    </section>

    <div class="admin-grid">
      <section class="panel folders">
        <header class="panel-header">
          <span>Library folders</span>
          <span class="panel-hint">{{ folders.length ? `${folders.length} selected` : 'Whole root' }}</span>
        </header>
        <div class="panel-body">
          <p class="field-hint">
            Root: <span class="mono">{{ config?.mediaDir || '—' }}</span>. Only the folders below are
            indexed; anything else under the root is pruned on scan. Leave empty to index everything.
          </p>
          <v-combobox
            v-model="folders"
            :items="config?.available || []"
            label="Folders to index"
            multiple
            chips
            closable-chips
            clearable
            density="compact"
            variant="outlined"
            placeholder="e.g. movies, tv"
            prepend-inner-icon="mdi-folder-multiple-outline"
          />
          <div class="field-actions">
            <button type="button" class="btn-secondary" :disabled="!dirty || savingConfig" @click="saveFolders">
              {{ savingConfig ? 'Saving…' : 'Save folders' }}
            </button>
            <span v-if="dirty" class="dirty-hint">Unsaved changes</span>
          </div>
          <p v-if="configError" class="error-msg">{{ configError }}</p>
        </div>
      </section>

      <section class="panel operations">
        <header class="panel-header">
          <span>Operations</span>
        </header>
        <div class="panel-body ops">
          <div class="op-row">
            <div class="op-copy">
              <span class="op-title">Scan library</span>
              <span class="op-sub">Walk the selected folders, index new or changed files.</span>
            </div>
            <button type="button" class="btn-primary" :disabled="scanning" @click="doScan">
              {{ scanning ? 'Starting…' : 'Scan' }}
            </button>
          </div>
          <div class="op-row">
            <div class="op-copy">
              <span class="op-title">Rebuild search index</span>
              <span class="op-sub">Re-upsert every chunk from SQLite into Meilisearch.</span>
            </div>
            <button type="button" class="btn-secondary" :disabled="reindexing" @click="doReindex">
              {{ reindexing ? 'Starting…' : 'Rebuild' }}
            </button>
          </div>
          <div class="op-row">
            <div class="op-copy">
              <span class="op-title">Re-probe processed files</span>
              <span class="op-sub">Re-run metadata, audio and subtitle selection.</span>
            </div>
            <button type="button" class="btn-secondary" :disabled="reprobing" @click="doReprobe">
              {{ reprobing ? 'Starting…' : 'Re-probe' }}
            </button>
          </div>

          <div class="op-row queue-row">
            <div class="op-copy">
              <span class="op-title">Queue control</span>
              <span class="op-sub">
                {{ paused ? 'Workers are paused — nothing new is claimed.' : 'Pause workers or stop and clear everything queued.' }}
              </span>
            </div>
            <div class="queue-actions">
              <button type="button" class="btn-secondary" @click="togglePause">
                {{ paused ? 'Resume' : 'Pause' }}
              </button>
              <button type="button" class="btn-danger" @click="confirmOpen = true">Stop &amp; clear</button>
            </div>
          </div>

          <div v-if="snap?.scan" class="scan-progress">
            <div class="scan-labels">
              <span class="mono truncate">{{ snap.scan.rootPath }}</span>
              <span>{{ snap.scan.filesDone }}/{{ snap.scan.filesSeen }}</span>
            </div>
            <div class="stat-progress"><div class="fill" :style="{ width: `${scanPct}%` }"></div></div>
          </div>
        </div>
      </section>

      <section class="panel activity">
        <header class="panel-header">
          <span>Processing now</span>
          <span class="panel-hint">{{ running.length }}</span>
        </header>
        <div class="panel-body activity-body">
          <p v-if="!running.length" class="idle">Nothing is being decoded right now.</p>
          <div v-for="job in running" :key="job.id" class="activity-row">
            <span class="type">{{ job.type }}</span>
            <div class="activity-main">
              <span class="activity-title truncate">{{ job.mediaTitle || fileBase(job.mediaPath) || job.type }}</span>
              <span class="activity-path mono truncate">{{ job.mediaPath || job.payload }}</span>
            </div>
            <span class="elapsed mono">{{ elapsed(job) }}</span>
          </div>
        </div>
      </section>
    </div>

    <RecentJobsPanel :jobs="recentJobs" />

    <v-dialog v-model="confirmOpen" max-width="420">
      <div class="confirm-panel">
        <h2 class="confirm-title">Stop and clear the queue?</h2>
        <p class="confirm-copy">
          Workers pause immediately, in-flight jobs are aborted, and every queued job is deleted.
          The library stops processing until you resume.
        </p>
        <div class="confirm-actions">
          <button type="button" class="btn-secondary" @click="confirmOpen = false">Cancel</button>
          <button type="button" class="btn-danger" :disabled="stopping" @click="stopAndClear">
            {{ stopping ? 'Stopping…' : 'Stop & clear' }}
          </button>
        </div>
      </div>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useNow } from '@vueuse/core'
import { api, type AdminConfig, type AdminSnapshot, type Job } from '../api'
import RecentJobsPanel from './admin/RecentJobsPanel.vue'
import StatCard from './admin/StatCard.vue'
import { fmtBytes } from '../lib/format'
import { fileBase, jobElapsed } from '../lib/jobs'
import { percent, type StatProgressModel } from '../lib/progress'

const now = useNow({ interval: 1000 })

const snap = ref<AdminSnapshot | null>(null)
const config = ref<AdminConfig | null>(null)
const folders = ref<string[]>([])
const savingConfig = ref(false)
const configError = ref('')
const scanning = ref(false)
const reindexing = ref(false)
const reprobing = ref(false)
const confirmOpen = ref(false)
const stopping = ref(false)
let stream: EventSource | null = null
let poller: number | null = null

const counts = computed(() => snap.value?.mediaCounts ?? {})
const total = computed(() => Object.values(counts.value).reduce((sum, n) => sum + n, 0))
const running = computed(() => snap.value?.runningJobs ?? [])
const paused = computed(() => snap.value?.queue?.paused ?? false)
const dirty = computed(() => {
  const saved = config.value?.folders ?? []
  const current = folders.value ?? []
  return saved.length !== current.length || saved.some((f) => !current.includes(f))
})
const scanPct = computed(() => {
  const scan = snap.value?.scan
  if (!scan) return 0
  if (!scan.filesSeen) return 5
  return percent(scan.filesDone, scan.filesSeen)
})

// --- stat card progress ------------------------------------------------------
// Every card renders the same bottom-pinned footer (unit line + bar) so the
// bars share one baseline across the row. `empty` means nothing is countable
// yet: a bare track and a dash, never a completion claim. The queue is the one
// deliberate exception — an empty queue is a clear work list, so it reads full.

const pendingCount = computed(() => n('pending'))
// Files that reached a terminal state: searchable, no subtitles, or failed.
const processedCount = computed(() => n('indexed') + n('no_subs') + n('error'))
const recentJobs = computed(() => snap.value?.recentJobs ?? [])

// Work that can still add media rows or dialogue chunks: a scan, pending
// probes, queued jobs, or a running probe/index job. Clip renders never touch
// dialogue, so they must not read as indexing activity.
const mediaWorkActive = computed(() => {
  if (snap.value?.scan) return true
  if (pendingCount.value > 0) return true
  if ((snap.value?.queue?.queued ?? 0) > 0) return true
  return (snap.value?.runningJobs ?? []).some((job) => job.type === 'probe' || job.type === 'index')
})

type DialogueState = 'empty' | 'waiting' | 'keeping-up' | 'settled'
interface DialogueView {
  state: DialogueState
  sub: string
  progress: StatProgressModel
}

// One model for the whole Dialogue card: the sub line and the bar derive from
// the same state instead of re-testing parallel boolean flags.
const dialogue = computed<DialogueView>(() => {
  const chunks = snap.value?.lines?.chunks ?? 0
  const indexed = snap.value?.lines?.chunksIndexed ?? 0
  let state: DialogueState
  if (!chunks) state = 'empty'
  else if (indexed < chunks) state = 'waiting'
  else state = mediaWorkActive.value ? 'keeping-up' : 'settled'

  const label = `${indexed.toLocaleString()} of ${chunks.toLocaleString()} chunks indexed`
  const sub = {
    empty: 'No dialogue indexed yet',
    waiting: `${(chunks - indexed).toLocaleString()} chunks waiting to be indexed`,
    'keeping-up': 'index keeping up with processing',
    settled: 'All chunks searchable',
  }[state]

  let progress: StatProgressModel
  if (state === 'empty') progress = { label, pct: 0, empty: true }
  else if (state === 'keeping-up') progress = { label, pct: percent(indexed, chunks), indeterminate: true }
  else progress = { label, pct: percent(indexed, chunks) }

  return { state, sub, progress }
})

const libraryProgress = computed<StatProgressModel>(() => {
  if (!total.value) return { label: '0 of 0 files processed', pct: 0, empty: true }
  return {
    label: `${processedCount.value.toLocaleString()} of ${total.value.toLocaleString()} files processed`,
    pct: percent(processedCount.value, total.value),
  }
})

// The unit of work is one job reaching a terminal status: done or failed.
// An empty queue renders full because there is nothing left to do.
const queueProgress = computed<StatProgressModel>(() => {
  const finished = (snap.value?.queue?.done ?? 0) + (snap.value?.queue?.error ?? 0)
  const known = finished + (snap.value?.queue?.queued ?? 0) + (snap.value?.queue?.running ?? 0)
  return {
    label: `${finished.toLocaleString()} of ${known.toLocaleString()} jobs finished`,
    pct: known ? percent(finished, known) : 100,
  }
})

// The unit is one byte of the configured cache quota; CACHE_MAX_GB=0 means
// unlimited, which is shown as usage without a fraction.
const cacheProgress = computed<StatProgressModel>(() => {
  const used = snap.value?.cacheBytes ?? 0
  const quota = config.value?.cacheMaxBytes ?? 0
  if (!quota) return { label: `${fmtBytes(used)} used · no limit`, pct: 0, empty: true }
  return { label: `${fmtBytes(used)} of ${fmtBytes(quota)} used`, pct: percent(used, quota) }
})

let lastRawSnap = ''

function connectStream() {
  if (stream) stream.close()
  stream = new EventSource('/api/admin/events')
  
  stream.onopen = () => {
    if (poller) {
      window.clearInterval(poller)
      poller = null
    }
  }

  stream.addEventListener('tick', (event) => {
    try {
      const raw = (event as MessageEvent).data
      if (raw === lastRawSnap) return
      lastRawSnap = raw
      snap.value = JSON.parse(raw)
    } catch {
      /* ignore malformed ticks */
    }
  })

  stream.onerror = () => {
    // EventSource auto-reconnects, but if we wanted manual backoff we'd do it here.
    // The browser's native EventSource reconnection is usually sufficient.
  }
}

onMounted(() => {
  connectStream()
  void loadConfig()
})

onUnmounted(() => {
  stream?.close()
  if (poller) window.clearInterval(poller)
})

function n(key: string): number {
  return counts.value[key] ?? 0
}

async function loadConfig() {
  try {
    config.value = await api.config()
    folders.value = config.value.folders ?? []
  } catch (err) {
    configError.value = (err as Error).message
  }
}

async function saveFolders() {
  savingConfig.value = true
  configError.value = ''
  try {
    const res = await api.saveConfig((folders.value ?? []).map((f) => String(f).trim()).filter(Boolean))
    if (config.value) config.value.folders = res.folders
  } catch (err) {
    configError.value = (err as Error).message
  } finally {
    savingConfig.value = false
  }
}

async function doScan() {
  scanning.value = true
  try {
    await api.scan('')
  } finally {
    scanning.value = false
  }
}

async function doReindex() {
  reindexing.value = true
  try {
    await api.reindex()
  } finally {
    reindexing.value = false
  }
}

async function togglePause() {
  if (paused.value) await api.resumeQueue()
  else await api.pauseQueue()
}

async function stopAndClear() {
  stopping.value = true
  try {
    await api.stopQueue()
    confirmOpen.value = false
  } finally {
    stopping.value = false
  }
}

async function doReprobe() {
  reprobing.value = true
  try {
    await api.reprobe()
  } finally {
    reprobing.value = false
  }
}

function elapsed(job: Job): string {
  return jobElapsed(job, now.value.getTime())
}
</script>

<style scoped>
.page {
  max-width: 1520px;
  margin: 0 auto;
  padding: 28px 24px 64px;
}
.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 22px;
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
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.stat-error {
  color: #fca5a5;
}
.stat-note {
  color: var(--accent);
}
.admin-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
  margin-bottom: 16px;
}
@media (max-width: 1100px) {
  .admin-grid {
    grid-template-columns: 1fr;
  }
}
.panel {
  display: flex;
  flex-direction: column;
}
.panel-hint {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--faint);
}
.panel-body {
  padding: 14px 16px 16px;
}
.field-hint {
  margin: 0 0 12px;
  font-size: 12px;
  line-height: 1.55;
  color: var(--faint);
}
.field-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}
.dirty-hint {
  font-size: 11.5px;
  color: var(--accent);
}
.btn-primary {
  padding: 8px 14px;
  border-radius: 8px;
  border: none;
  background: var(--accent);
  color: #151515;
  font-size: 12.5px;
  font-weight: 650;
}
.btn-primary:hover {
  background: var(--accent-strong);
}
.btn-primary:disabled {
  opacity: 0.6;
}
.btn-secondary {
  padding: 8px 13px;
  border-radius: 8px;
  border: 1px solid var(--border-strong);
  background: transparent;
  color: var(--text);
  font-size: 12.5px;
}
.btn-secondary:hover {
  background: rgba(255, 255, 255, 0.05);
}
.btn-secondary:disabled {
  opacity: 0.55;
}
.ops {
  padding-top: 6px;
}
.op-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 11px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}
.op-row:last-of-type {
  border-bottom: none;
}
.op-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.op-title {
  font-size: 13px;
  font-weight: 550;
  color: var(--text);
}
.op-sub {
  font-size: 11.5px;
  color: var(--faint);
  line-height: 1.4;
}
.scan-progress {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}
.scan-labels {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 6px;
}
.activity-body {
  display: flex;
  flex-direction: column;
}
.activity-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}
.activity-row:last-child {
  border-bottom: none;
}
.type {
  flex: none;
  font-family: var(--font-mono);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--faint);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 1px 5px;
}
.activity-main {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
}
.activity-title {
  font-size: 12.5px;
  color: var(--text);
}
.activity-path {
  font-size: 10.5px;
  color: var(--faint);
}
.elapsed {
  font-size: 11px;
  color: var(--muted);
}
.idle {
  margin: 8px 0;
  font-size: 12px;
  color: var(--faint);
}
.queue-actions {
  display: flex;
  gap: 8px;
  flex: none;
}
.btn-danger {
  padding: 8px 13px;
  border-radius: 8px;
  border: 1px solid rgba(248, 113, 113, 0.4);
  background: transparent;
  color: #fca5a5;
  font-size: 12.5px;
}
.btn-danger:hover {
  background: rgba(248, 113, 113, 0.1);
}
.btn-danger:disabled {
  opacity: 0.6;
}
.paused-tag {
  display: inline-block;
  font-family: var(--font-mono);
  font-size: 10.5px;
  padding: 1px 6px;
  border-radius: 4px;
  color: var(--accent);
  border: 1px solid var(--accent-line);
  background: var(--accent-soft);
}
.confirm-panel {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 20px;
}
.confirm-title {
  margin: 0 0 8px;
  font-size: 17px;
  font-weight: 650;
}
.confirm-copy {
  margin: 0 0 18px;
  font-size: 13px;
  line-height: 1.55;
  color: var(--muted);
}
.confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.error-msg {
  margin: 10px 0 0;
  padding: 8px 12px;
  border: 1px solid rgba(248, 113, 113, 0.35);
  background: rgba(248, 113, 113, 0.08);
  border-radius: 8px;
  font-size: 12.5px;
  color: #fca5a5;
}
</style>
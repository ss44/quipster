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
      <div class="stat">
        <p class="stat-label">Library</p>
        <p class="stat-value">{{ total.toLocaleString() }}<span class="stat-unit">files</span></p>
        <p class="stat-sub">
          {{ n('indexed').toLocaleString() }} searchable · {{ n('pending').toLocaleString() }} queued ·
          {{ n('no_subs').toLocaleString() }} without subs
          <span v-if="n('error')" class="stat-error">· {{ n('error') }} failed</span>
        </p>
      </div>
      <div class="stat">
        <p class="stat-label">Dialogue</p>
        <p class="stat-value">{{ (snap?.lines?.cues ?? 0).toLocaleString() }}<span class="stat-unit">lines</span></p>
        <p class="stat-sub">
          {{ (snap?.lines?.chunksIndexed ?? 0).toLocaleString() }} of
          {{ (snap?.lines?.chunks ?? 0).toLocaleString() }} chunks indexed
        </p>
        <div class="stat-progress"><div class="fill" :style="{ width: `${indexedPct}%` }"></div></div>
      </div>
      <div class="stat">
        <p class="stat-label">Queue</p>
        <p class="stat-value">{{ (snap?.queue?.queued ?? 0).toLocaleString() }}<span class="stat-unit">waiting</span></p>
        <p class="stat-sub">
          <span v-if="paused" class="paused-tag">Paused</span>
          <template v-else>
            {{ snap?.queue?.running ?? 0 }} running · {{ snap?.queue?.done ?? 0 }} done
            <span v-if="snap?.queue?.error" class="stat-error">· {{ snap.queue.error }} failed</span>
          </template>
        </p>
      </div>
      <div class="stat">
        <p class="stat-label">Clip cache</p>
        <p class="stat-value">{{ fmtBytes(snap?.cacheBytes ?? 0) }}</p>
        <p class="stat-sub">Clips, thumbnails and extracted subtitles, LRU-evicted.</p>
      </div>
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

    <section class="panel jobs">
      <header class="panel-header">
        <span>Recent jobs</span>
        <button type="button" class="link-btn" @click="refreshJobs">Refresh</button>
      </header>
      <div class="table-wrap">
        <table class="jobs-table">
          <thead>
            <tr>
              <th>Type</th>
              <th>File</th>
              <th>Status</th>
              <th>Lines</th>
              <th>Took</th>
              <th>Created</th>
              <th>Error</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="job in jobs" :key="job.id">
              <td class="mono">{{ job.type }}</td>
              <td>
                <span class="file-title truncate">{{ job.mediaTitle || fileBase(job.mediaPath) || '—' }}</span>
                <span class="file-path mono truncate">{{ fileBase(job.mediaPath) }}</span>
              </td>
              <td>
                <span class="state"><span class="status-dot" :class="statusClass(job.status)"></span>{{ job.status }}</span>
              </td>
              <td class="mono">{{ job.mediaCues ? job.mediaCues : '—' }}</td>
              <td class="mono">{{ fmtDur(job) }}</td>
              <td class="mono">{{ fmtTime(job.createdAt) }}</td>
              <td>
                <span v-if="job.error" class="error-text truncate" :title="job.error">{{ job.error }}</span>
                <span v-else class="faint">—</span>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!jobs.length" class="idle">No jobs yet — run a scan to populate the queue.</p>
      </div>
    </section>

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
import { api, type AdminConfig, type AdminSnapshot, type Job } from '../api'

const snap = ref<AdminSnapshot | null>(null)
const jobs = ref<Job[]>([])
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
  if (!scan || !scan.filesSeen) return scan ? 5 : 0
  return Math.min(100, Math.round((scan.filesDone / Math.max(scan.filesSeen, 1)) * 100))
})
const indexedPct = computed(() => {
  const lines = snap.value?.lines
  if (!lines || !lines.chunks) return 0
  return Math.round((lines.chunksIndexed / lines.chunks) * 100)
})

onMounted(() => {
  stream = new EventSource('/api/admin/events')
  stream.addEventListener('tick', (event) => {
    try {
      snap.value = JSON.parse((event as MessageEvent).data)
    } catch {
      /* ignore malformed ticks */
    }
  })
  stream.onerror = () => {
    if (!poller) poller = window.setInterval(refreshJobs, 5000)
  }
  void loadConfig()
  void refreshJobs()
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

async function refreshJobs() {
  try {
    jobs.value = (await api.jobs()).jobs ?? []
  } catch {
    /* admin may be token-protected */
  }
}

async function doScan() {
  scanning.value = true
  try {
    await api.scan('')
    await refreshJobs()
  } finally {
    scanning.value = false
  }
}

async function doReindex() {
  reindexing.value = true
  try {
    await api.reindex()
    await refreshJobs()
  } finally {
    reindexing.value = false
  }
}

async function togglePause() {
  if (paused.value) await api.resumeQueue()
  else await api.pauseQueue()
  await refreshJobs()
}

async function stopAndClear() {
  stopping.value = true
  try {
    await api.stopQueue()
    confirmOpen.value = false
    await refreshJobs()
  } finally {
    stopping.value = false
  }
}

async function doReprobe() {
  reprobing.value = true
  try {
    await api.reprobe()
    await refreshJobs()
  } finally {
    reprobing.value = false
  }
}

function statusClass(status: string): string {
  switch (status) {
    case 'done':
      return 'ok'
    case 'error':
      return 'err'
    case 'running':
      return 'run'
    default:
      return ''
  }
}

function fileBase(path: string): string {
  if (!path) return ''
  const parts = path.split('/')
  return parts[parts.length - 1]
}

function elapsed(job: Job): string {
  if (!job.startedAt) return ''
  const end = job.finishedAt || Date.now()
  const ms = Math.max(0, end - job.startedAt)
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${Math.round(ms / 1000)}s`
  return `${(ms / 60000).toFixed(1)}m`
}

function fmtTime(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function fmtDur(job: Job): string {
  if (!job.startedAt) return '—'
  if (!job.finishedAt) return job.status === 'running' ? 'running' : '—'
  return elapsed(job)
}

function fmtBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(1)} ${units[unit]}`
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
.stat {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 14px 16px;
}
.stat-label {
  margin: 0 0 8px;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--faint);
}
.stat-value {
  margin: 0;
  font-size: 24px;
  font-weight: 650;
  letter-spacing: -0.02em;
  line-height: 1.1;
}
.stat-unit {
  font-size: 12px;
  font-weight: 500;
  color: var(--faint);
  margin-left: 6px;
  letter-spacing: 0;
}
.stat-sub {
  margin: 6px 0 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--muted);
}
.stat-error {
  color: #fca5a5;
}
.stat-progress {
  height: 3px;
  margin-top: 10px;
  border-radius: 2px;
  background: rgba(255, 255, 255, 0.07);
  overflow: hidden;
}
.stat-progress .fill {
  height: 100%;
  background: var(--accent);
  border-radius: 2px;
  transition: width 0.4s ease;
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
.jobs {
  margin-top: 0;
}
.table-wrap {
  overflow-x: auto;
}
.jobs-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.jobs-table th {
  text-align: left;
  padding: 9px 14px;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--faint);
  border-bottom: 1px solid var(--border);
}
.jobs-table td {
  padding: 9px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.045);
  vertical-align: top;
}
.jobs-table tr:last-child td {
  border-bottom: none;
}
.file-title {
  display: block;
  max-width: 280px;
  color: var(--text);
  font-size: 12px;
}
.file-path {
  display: block;
  max-width: 280px;
  color: var(--faint);
  font-size: 10.5px;
}
.state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  color: var(--muted);
  text-transform: lowercase;
}
.error-text {
  display: inline-block;
  max-width: 230px;
  color: #fca5a5;
}
.link-btn {
  background: none;
  border: none;
  padding: 0;
  font-size: 12px;
  color: var(--muted);
  text-decoration: underline;
  text-underline-offset: 3px;
}
.link-btn:hover {
  color: var(--text);
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
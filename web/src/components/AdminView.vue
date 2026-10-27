<template>
  <v-container fluid class="pa-4 pa-md-6" max-width="1500">
    <div class="page-head d-flex align-center flex-wrap gap-3">
      <div>
        <div class="text-h5 font-weight-bold">Library admin</div>
        <div class="text-caption text-medium-emphasis d-flex align-center gap-2 mt-1">
          <span class="pulse-dot" />
          Live updates every second
        </div>
      </div>
      <v-spacer />
      <v-chip :color="snap?.meiliUp ? 'success' : 'error'" variant="tonal" prepend-icon="mdi-magnify-scan">
        Meilisearch {{ snap?.meiliUp ? 'online' : 'offline' }}
      </v-chip>
    </div>

    <v-row class="mt-2">
      <v-col cols="12" sm="6" lg="3">
        <v-card class="stat-card h-100">
          <v-card-text>
            <div class="stat-label"><v-icon icon="mdi-movie-open-outline" size="16" class="mr-1" />Library</div>
            <div class="stat-value">{{ total.toLocaleString() }}<span class="stat-unit">files</span></div>
            <div class="chips">
              <v-chip size="x-small" variant="tonal" color="success">indexed {{ n('indexed') }}</v-chip>
              <v-chip size="x-small" variant="tonal" color="warning">pending {{ n('pending') }}</v-chip>
              <v-chip size="x-small" variant="tonal">no subs {{ n('no_subs') }}</v-chip>
              <v-chip size="x-small" variant="tonal" color="error">error {{ n('error') }}</v-chip>
            </div>
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" sm="6" lg="3">
        <v-card class="stat-card h-100">
          <v-card-text>
            <div class="stat-label"><v-icon icon="mdi-format-quote-close" size="16" class="mr-1" />Dialogue digested</div>
            <div class="stat-value">{{ (snap?.lines?.cues ?? 0).toLocaleString() }}<span class="stat-unit">lines</span></div>
            <div class="text-caption text-medium-emphasis mb-2">
              {{ (snap?.lines?.chunks ?? 0).toLocaleString() }} chunks ·
              {{ (snap?.lines?.chunksIndexed ?? 0).toLocaleString() }} searchable
            </div>
            <v-progress-linear
              :model-value="indexedPct"
              color="primary"
              height="5"
              rounded
            />
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" sm="6" lg="3">
        <v-card class="stat-card h-100">
          <v-card-text>
            <div class="stat-label"><v-icon icon="mdi-progress-wrench" size="16" class="mr-1" />Job queue</div>
            <div class="stat-value">{{ (snap?.queue?.queued ?? 0).toLocaleString() }}<span class="stat-unit">queued</span></div>
            <div class="chips">
              <v-chip size="x-small" variant="tonal" color="info">running {{ snap?.queue?.running ?? 0 }}</v-chip>
              <v-chip size="x-small" variant="tonal" color="success">done {{ snap?.queue?.done ?? 0 }}</v-chip>
              <v-chip size="x-small" variant="tonal" color="error">error {{ snap?.queue?.error ?? 0 }}</v-chip>
            </div>
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" sm="6" lg="3">
        <v-card class="stat-card h-100">
          <v-card-text>
            <div class="stat-label"><v-icon icon="mdi-harddisk" size="16" class="mr-1" />Clip cache</div>
            <div class="stat-value">{{ fmtBytes(snap?.cacheBytes ?? 0) }}</div>
            <div class="text-caption text-medium-emphasis">
              clips, thumbnails, extracted subtitles · LRU-evicted
            </div>
          </v-card-text>
        </v-card>
      </v-col>
    </v-row>

    <v-row>
      <v-col cols="12" lg="5">
        <v-card class="stat-card h-100">
          <v-card-title class="text-subtitle-1">Scan library</v-card-title>
          <v-card-text>
            <v-text-field
              v-model="scanRoot"
              label="Subfolder to scan (optional, e.g. movies)"
              density="compact"
              hide-details
              clearable
              prepend-inner-icon="mdi-folder-outline"
              class="mb-3"
            />
            <div class="d-flex gap-2 flex-wrap">
              <v-btn color="primary" prepend-icon="mdi-folder-search" :loading="scanning" @click="doScan">
                Scan library
              </v-btn>
              <v-btn variant="tonal" prepend-icon="mdi-database-refresh" :loading="reindexing" @click="doReindex">
                Rebuild search index
              </v-btn>
              <v-btn variant="tonal" prepend-icon="mdi-refresh-circle" :loading="reprobing" @click="doReprobe">
                Re-probe files
              </v-btn>
            </div>

            <template v-if="snap?.scan">
              <div class="scan-line mt-4">
                <div class="d-flex justify-between text-caption" style="gap: 10px">
                  <span class="mono clamp-1">{{ snap.scan.rootPath }}</span>
                  <span class="flex-shrink-0">{{ snap.scan.filesDone }}/{{ snap.scan.filesSeen }}</span>
                </div>
                <v-progress-linear :model-value="scanPct" color="primary" height="6" rounded class="mt-1" />
              </div>
            </template>

            <div v-if="errorJobs.length" class="error-block mt-4">
              <div class="text-caption font-weight-bold text-error mb-1">
                <v-icon icon="mdi-alert-circle-outline" size="14" class="mr-1" />Recent failures
              </div>
              <div v-for="j in errorJobs" :key="j.id" class="error-row">
                <span class="mono text-caption">{{ j.type }} #{{ j.id }}</span>
                <span class="text-caption text-medium-emphasis clamp-1">{{ j.mediaTitle || j.error }}</span>
                <v-tooltip :text="j.error" max-width="420">
                  <template #activator="{ props }">
                    <v-icon v-bind="props" icon="mdi-information-outline" size="14" class="ml-1" />
                  </template>
                </v-tooltip>
              </div>
            </div>
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" lg="7">
        <v-card class="stat-card h-100">
          <v-card-title class="text-subtitle-1 d-flex align-center">
            <v-icon icon="mdi-activity" size="18" class="mr-2 text-primary" />
            Processing now
            <v-chip v-if="running.length" size="x-small" variant="tonal" class="ml-2">{{ running.length }}</v-chip>
          </v-card-title>
          <v-card-text class="pt-1">
            <div v-if="!running.length" class="text-caption text-medium-emphasis py-6 text-center">
              Idle — nothing is being decoded right now.
            </div>
            <div v-for="j in running" :key="j.id" class="activity-row">
              <v-icon :icon="typeIcon(j.type)" size="16" class="text-primary mr-2" />
              <div class="activity-main">
                <div class="d-flex justify-between" style="gap: 10px">
                  <div class="text-body-2 clamp-1">
                    {{ j.mediaTitle || fileBase(j.mediaPath) || j.type }}
                  </div>
                  <span class="text-caption text-medium-emphasis flex-shrink-0">
                    {{ j.type }} #{{ j.id }} · {{ elapsed(j) }}
                  </span>
                </div>
                <div v-if="j.type === 'clip' && j.progress > 0" class="mt-1">
                  <v-progress-linear :model-value="Math.round(j.progress * 100)" height="4" rounded color="primary" />
                </div>
                <div v-else class="text-caption text-medium-emphasis clamp-1 mono">
                  {{ j.mediaPath || j.payload }}
                </div>
              </div>
            </div>
          </v-card-text>
        </v-card>
      </v-col>
    </v-row>

    <v-row>
      <v-col cols="12">
        <v-card class="stat-card">
          <v-card-title class="text-subtitle-1 d-flex align-center">
            Recent jobs
            <v-spacer />
            <v-btn size="small" variant="text" prepend-icon="mdi-refresh" @click="refreshJobs">Refresh</v-btn>
          </v-card-title>
          <v-data-table
            :headers="headers"
            :items="jobs"
            density="compact"
            :items-per-page="12"
            class="bg-transparent"
          >
            <template #item.file="{ item }">
              <div class="clamp-1 text-body-2">{{ item.mediaTitle || fileBase(item.mediaPath) || '—' }}</div>
              <div class="text-caption text-medium-emphasis clamp-1 mono">{{ fileBase(item.mediaPath) }}</div>
            </template>
            <template #item.type="{ value }">
              <v-chip size="x-small" variant="tonal" :prepend-icon="typeIcon(value)">{{ value }}</v-chip>
            </template>
            <template #item.status="{ value }">
              <v-chip :color="statusColor(value)" size="small" variant="tonal">{{ value }}</v-chip>
            </template>
            <template #item.lines="{ item }">
              <span v-if="item.mediaCues" class="text-caption">{{ item.mediaCues }} lines</span>
              <span v-else class="text-caption text-medium-emphasis">—</span>
            </template>
            <template #item.duration="{ item }">
              <span class="text-caption">{{ fmtDur(item) }}</span>
            </template>
            <template #item.createdAt="{ value }">
              <span class="text-caption">{{ fmtTime(value) }}</span>
            </template>
            <template #item.error="{ value }">
              <v-tooltip v-if="value" :text="value" max-width="420">
                <template #activator="{ props }">
                  <span v-bind="props" class="text-caption text-error clamp-1 d-inline-block" style="max-width: 180px">
                    {{ value }}
                  </span>
                </template>
              </v-tooltip>
              <span v-else class="text-medium-emphasis">—</span>
            </template>
          </v-data-table>
        </v-card>
      </v-col>
    </v-row>
  </v-container>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type AdminSnapshot, type Job } from '../api'

const snap = ref<AdminSnapshot | null>(null)
const jobs = ref<Job[]>([])
const scanRoot = ref('')
const scanning = ref(false)
const reindexing = ref(false)
const reprobing = ref(false)
let es: EventSource | null = null
let poller: number | null = null

const headers = [
  { title: 'File', key: 'file', sortable: false },
  { title: 'Type', key: 'type', width: 110 },
  { title: 'Status', key: 'status', width: 110 },
  { title: 'Lines', key: 'lines', width: 100, sortable: false },
  { title: 'Attempts', key: 'attempts', width: 90 },
  { title: 'Duration', key: 'duration', width: 110, sortable: false },
  { title: 'Created', key: 'createdAt', width: 170 },
  { title: 'Error', key: 'error', sortable: false },
]

const counts = computed(() => snap.value?.mediaCounts ?? {})
const total = computed(() => Object.values(counts.value).reduce((a, b) => a + b, 0))
const running = computed(() => snap.value?.runningJobs ?? [])
const errorJobs = computed(() => (snap.value?.recentJobs ?? []).filter((j) => j.status === 'error').slice(0, 4))
const scanPct = computed(() => {
  const s = snap.value?.scan
  if (!s || !s.filesSeen) return s ? 5 : 0
  return Math.min(100, Math.round((s.filesDone / Math.max(s.filesSeen, 1)) * 100))
})
const indexedPct = computed(() => {
  const l = snap.value?.lines
  if (!l || !l.chunks) return 0
  return Math.round((l.chunksIndexed / l.chunks) * 100)
})

onMounted(() => {
  es = new EventSource('/api/admin/events')
  es.addEventListener('tick', (ev) => {
    try {
      snap.value = JSON.parse((ev as MessageEvent).data)
    } catch {
      /* ignore malformed tick */
    }
  })
  es.onerror = () => {
    if (!poller) poller = window.setInterval(refreshJobs, 5000)
  }
  refreshJobs()
})

onUnmounted(() => {
  es?.close()
  if (poller) window.clearInterval(poller)
})

function n(key: string): number {
  return counts.value[key] ?? 0
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
    await api.scan(scanRoot.value.trim())
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

async function doReprobe() {
  reprobing.value = true
  try {
    await api.reprobe()
    await refreshJobs()
  } finally {
    reprobing.value = false
  }
}

function typeIcon(t: string): string {
  switch (t) {
    case 'scan':
      return 'mdi-folder-search'
    case 'probe':
      return 'mdi-subtitles-outline'
    case 'index':
      return 'mdi-magnify-scan'
    case 'clip':
      return 'mdi-content-cut'
    default:
      return 'mdi-cog'
  }
}

function statusColor(s: string): string {
  switch (s) {
    case 'done':
      return 'success'
    case 'error':
      return 'error'
    case 'running':
      return 'info'
    default:
      return 'default'
  }
}

function fileBase(p: string): string {
  if (!p) return ''
  const parts = p.split('/')
  return parts[parts.length - 1]
}

function elapsed(j: Job): string {
  if (!j.startedAt) return ''
  const end = j.finishedAt || Date.now()
  const d = Math.max(0, end - j.startedAt)
  if (d < 1000) return `${d}ms`
  if (d < 60000) return `${(d / 1000).toFixed(0)}s`
  return `${(d / 60000).toFixed(1)}m`
}

function fmtTime(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function fmtDur(j: Job): string {
  if (!j.startedAt) return '—'
  if (!j.finishedAt) return j.status === 'running' ? 'running…' : '—'
  return elapsed(j)
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let u = 0
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024
    u++
  }
  return `${v.toFixed(1)} ${units[u]}`
}
</script>

<style scoped>
.page-head {
  margin-bottom: 8px;
}
.stat-label {
  display: flex;
  align-items: center;
  font-size: 12px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: rgba(255, 255, 255, 0.5);
  margin-bottom: 6px;
}
.stat-value {
  font-size: 26px;
  font-weight: 700;
  line-height: 1.1;
}
.stat-unit {
  font-size: 13px;
  font-weight: 500;
  color: rgba(255, 255, 255, 0.45);
  margin-left: 6px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 10px;
}
.scan-line .mono {
  max-width: 60%;
}
.error-block {
  border-top: 1px solid rgba(255, 255, 255, 0.07);
  padding-top: 10px;
}
.error-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
}
.activity-row {
  display: flex;
  align-items: flex-start;
  padding: 8px 6px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.045);
}
.activity-row:last-child {
  border-bottom: none;
}
.activity-main {
  flex: 1;
  min-width: 0;
}
</style>
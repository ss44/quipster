<template>
  <v-container fluid class="pa-6">
    <v-row>
      <v-col cols="12" md="4">
        <v-card>
          <v-card-title class="text-subtitle-1">Library</v-card-title>
          <v-card-text>
            <div class="d-flex flex-wrap gap-2 mb-3">
              <v-chip variant="tonal" prepend-icon="mdi-movie">total {{ total }}</v-chip>
              <v-chip variant="tonal" color="success" prepend-icon="mdi-check">indexed {{ counts.indexed || 0 }}</v-chip>
              <v-chip variant="tonal" color="warning" prepend-icon="mdi-clock-outline">pending {{ counts.pending || 0 }}</v-chip>
              <v-chip variant="tonal" prepend-icon="mdi-close-circle-outline">no subs {{ counts.no_subs || 0 }}</v-chip>
              <v-chip variant="tonal" color="error" prepend-icon="mdi-alert">error {{ counts.error || 0 }}</v-chip>
            </div>
            <template v-if="snap?.scan">
              <div class="text-caption text-medium-emphasis mb-1">
                Scanning {{ snap.scan.root_path }} — {{ snap.scan.files_done }}/{{ snap.scan.files_seen }} files
              </div>
              <v-progress-linear
                :model-value="scanPct"
                color="primary"
                height="8"
                rounded
                class="mb-2"
              />
            </template>
            <v-text-field
              v-model="scanRoot"
              label="Scan subfolder (optional, e.g. movies)"
              hide-details
              density="compact"
              class="mb-2"
            />
            <div class="d-flex gap-2">
              <v-btn color="primary" prepend-icon="mdi-folder-search" :loading="scanning" @click="doScan">
                Scan library
              </v-btn>
              <v-btn variant="tonal" prepend-icon="mdi-refresh" @click="doReindex">Rebuild index</v-btn>
            </div>
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" md="4">
        <v-card>
          <v-card-title class="text-subtitle-1">Job queue</v-card-title>
          <v-card-text>
            <div class="d-flex flex-wrap gap-2 mb-2">
              <v-chip variant="tonal">queued {{ snap?.queue?.queued ?? 0 }}</v-chip>
              <v-chip variant="tonal" color="info">running {{ snap?.queue?.running ?? 0 }}</v-chip>
              <v-chip variant="tonal" color="success">done {{ snap?.queue?.done ?? 0 }}</v-chip>
              <v-chip variant="tonal" color="error">error {{ snap?.queue?.error ?? 0 }}</v-chip>
            </div>
            <v-chip :color="snap?.meiliUp ? 'success' : 'error'" variant="tonal" class="mt-1">
              meilisearch {{ snap?.meiliUp ? 'online' : 'offline' }}
            </v-chip>
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" md="4">
        <v-card>
          <v-card-title class="text-subtitle-1">Cache</v-card-title>
          <v-card-text>
            <div class="text-h6">{{ fmtBytes(snap?.cacheBytes ?? 0) }}</div>
            <div class="text-caption text-medium-emphasis">clips + thumbnails + extracted subs (LRU-evicted)</div>
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12">
        <v-card>
          <v-card-title class="text-subtitle-1">Recent jobs</v-card-title>
          <v-data-table
            :headers="headers"
            :items="jobs"
            density="compact"
            items-per-page="10"
            class="bg-transparent"
          >
            <template #item.status="{ value }">
              <v-chip :color="statusColor(value)" size="small" variant="tonal">{{ value }}</v-chip>
            </template>
            <template #item.error="{ value }">
              <span class="text-caption">{{ value }}</span>
            </template>
            <template #item.created_at="{ value }">{{ fmtTime(value) }}</template>
            <template #item.duration="{ item }">
              <span class="text-caption">{{ fmtDur(item) }}</span>
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
let es: EventSource | null = null
let poller: number | null = null

const headers = [
  { title: 'ID', key: 'id', width: 70 },
  { title: 'Type', key: 'type' },
  { title: 'Status', key: 'status' },
  { title: 'Attempts', key: 'attempts', width: 90 },
  { title: 'Duration', key: 'duration' },
  { title: 'Error', key: 'error' },
  { title: 'Created', key: 'created_at' },
]

const counts = computed(() => snap.value?.mediaCounts ?? {})
const total = computed(() => Object.values(counts.value).reduce((a, b) => a + b, 0))
const scanPct = computed(() => {
  const s = snap.value?.scan
  if (!s || !s.files_seen) return s ? 5 : 0
  return Math.min(100, Math.round((s.files_done / Math.max(s.files_seen, 1)) * 100))
})

onMounted(() => {
  es = new EventSource('/api/admin/events')
  es.addEventListener('tick', (ev) => {
    try {
      snap.value = JSON.parse((ev as MessageEvent).data)
    } catch {
      /* ignore */
    }
  })
  es.onerror = () => {
    // SSE failed (token?) — fall back to slow polling
    if (!poller) poller = window.setInterval(refreshJobs, 5000)
  }
  refreshJobs()
})
onUnmounted(() => {
  es?.close()
  if (poller) window.clearInterval(poller)
})

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
  await api.reindex()
  await refreshJobs()
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

function fmtTime(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleString()
}

function fmtDur(j: Job): string {
  if (!j.started_at || !j.finished_at) return j.status === 'running' ? 'running…' : '—'
  const d = Math.max(0, j.finished_at - j.started_at)
  return d < 1000 ? `${d}ms` : `${(d / 1000).toFixed(1)}s`
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
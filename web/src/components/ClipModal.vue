<template>
  <v-dialog v-model="open" max-width="1120" scrollable transition="dialog-bottom-transition">
    <v-card v-if="detail" class="clip-dialog" :loading="detailLoading">
      <div class="dialog-head d-flex align-center">
        <div class="min-width-0">
          <div class="text-subtitle-1 font-weight-bold clamp-1">
            {{ titleLine }}
          </div>
          <div class="text-caption text-medium-emphasis clamp-1 mono">{{ detail.media.path }}</div>
        </div>
        <v-spacer />
        <v-chip size="small" variant="tonal" class="mr-2" prepend-icon="mdi-clock-outline">
          {{ fmt(selStart) }} – {{ fmt(selEnd) }} · {{ (selEnd - selStart).toFixed(1) }}s
        </v-chip>
        <v-btn icon="mdi-close" variant="text" @click="open = false" aria-label="Close" />
      </div>

      <v-divider />

      <v-card-text class="pa-4">
        <v-row dense>
          <v-col cols="12" md="7">
            <div class="preview-shell">
              <video
                v-if="previewURL"
                :key="previewURL"
                :src="previewURL"
                controls
                autoplay
                class="preview"
              />
              <div v-else class="preview preview-empty">
                <v-progress-circular indeterminate />
              </div>
            </div>

            <div class="trim-block">
              <div class="d-flex align-center justify-between mb-1">
                <span class="text-caption text-medium-emphasis">
                  Trim by subtitle line · {{ selectedCount }} line{{ selectedCount === 1 ? '' : 's' }} selected
                </span>
                <v-btn
                  size="x-small"
                  variant="text"
                  prepend-icon="mdi-arrow-expand-horizontal"
                  @click="selectAll"
                >
                  Select all
                </v-btn>
              </div>
              <v-range-slider
                v-model="sel"
                :min="0"
                :max="Math.max(cues.length - 1, 0)"
                :step="1"
                tick
                show-ticks="always"
                tick-size="3"
                hide-details
                color="primary"
                @end="updatePreview"
              >
                <template #thumb-label="{ modelValue }">
                  {{ fmt(cues[Math.min(modelValue, cues.length - 1)]?.start ?? 0) }}
                </template>
              </v-range-slider>
              <div class="d-flex justify-between text-caption text-medium-emphasis mt-1">
                <span>{{ fmt(selStart) }}</span>
                <span>{{ fmt(selEnd) }}</span>
              </div>
            </div>

            <div class="actions-row">
              <v-switch
                v-model="accurate"
                color="primary"
                hide-details
                density="compact"
                :label="accurate ? 'Accurate cut (re-encode)' : 'Fast cut (lossless remux)'"
                :disabled="downloading"
              />
              <v-spacer />
              <v-btn variant="tonal" prepend-icon="mdi-link-variant" :disabled="downloading" @click="copyLink">
                Copy link
              </v-btn>
              <v-btn color="primary" prepend-icon="mdi-download" :loading="downloading" @click="download">
                Download
              </v-btn>
            </div>
            <div class="text-caption text-medium-emphasis mt-1">
              Fast cuts start at the previous keyframe (may include a few extra seconds of lead-in).
              Accurate re-encodes to 720p for frame-exact trimming.
            </div>

            <v-alert v-if="clipError" type="error" variant="tonal" density="compact" class="mt-3">
              {{ clipError }}
            </v-alert>

            <div v-if="jobProgress !== null" class="mt-3">
              <div class="d-flex justify-between text-caption mb-1">
                <span>{{ jobLabel }}</span>
                <span>{{ Math.round((jobProgress || 0) * 100) }}%</span>
              </div>
              <v-progress-linear
                :model-value="Math.round((jobProgress || 0) * 100)"
                color="primary"
                height="6"
                rounded
                :indeterminate="jobProgress === 0"
              />
            </div>
          </v-col>

          <v-col cols="12" md="5">
            <div class="cues-panel">
              <div class="text-caption text-medium-emphasis mb-2 d-flex align-center">
                <v-icon icon="mdi-subtitles-outline" size="15" class="mr-1" />
                Subtitles · click a line to adjust the trim
              </div>
              <div class="cue-scroll">
                <div
                  v-for="(c, i) in detail.contextBefore"
                  :key="'b' + c.idx"
                  class="cue-row outside text-caption pa-2"
                >
                  {{ c.text }}
                </div>
                <div
                  v-for="(c, i) in cues"
                  :key="c.idx"
                  class="cue-row pa-2"
                  :class="{ selected: i >= sel[0] && i <= sel[1] }"
                  @click="expandTo(i)"
                >
                  <div class="d-flex justify-between text-caption text-medium-emphasis mb-1">
                    <span>{{ fmt(c.start) }} → {{ fmt(c.end) }}</span>
                    <span v-if="i >= sel[0] && i <= sel[1]" class="text-primary">in clip</span>
                  </div>
                  <div class="text-body-2">{{ c.text }}</div>
                </div>
                <div
                  v-for="c in detail.contextAfter"
                  :key="'a' + c.idx"
                  class="cue-row outside text-caption pa-2"
                >
                  {{ c.text }}
                </div>
              </div>
            </div>
          </v-col>
        </v-row>
      </v-card-text>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, clipStreamURL, type ChunkDetail, type Hit } from '../api'

const open = ref(false)
const detail = ref<ChunkDetail | null>(null)
const detailLoading = ref(false)
const hit = ref<Hit | null>(null)
const sel = ref<[number, number]>([0, 0])
const accurate = ref(false)
const downloadPending = ref(false)
const clipError = ref('')
const jobProgress = ref<number | null>(null)
const jobLabel = ref('')
const previewURL = ref('')

const cues = computed(() => detail.value?.cues ?? [])
const selStart = computed(() => cues.value[Math.min(sel.value[0], cues.value.length - 1)]?.start ?? 0)
const selEnd = computed(() => cues.value[Math.min(sel.value[1], cues.value.length - 1)]?.end ?? 0)
const selectedCount = computed(() => sel.value[1] - sel.value[0] + 1)
const downloading = computed(() => downloadPending.value)
const titleLine = computed(() => {
  if (!detail.value) return ''
  const m = detail.value.media
  const base = m.showTitle || m.title
  const parts = [base]
  if (m.showTitle && m.season) parts.push(`S${pad(m.season)}E${pad(m.episode)}`)
  if (m.year) parts.push(`(${m.year})`)
  return parts.join(' ')
})

onMounted(() => window.addEventListener('open-clip', onOpenClip as EventListener))
onUnmounted(() => window.removeEventListener('open-clip', onOpenClip as EventListener))

async function onOpenClip(e: Event) {
  const h = (e as CustomEvent).detail as Hit
  await openWith(h)
}

async function openWith(h: Hit) {
  hit.value = h
  open.value = true
  detailLoading.value = true
  clipError.value = ''
  jobProgress.value = null
  try {
    detail.value = await api.chunk(h.chunkId)
    sel.value = [0, Math.max(cues.value.length - 1, 0)]
    updatePreview()
  } catch (err) {
    clipError.value = (err as Error).message
  } finally {
    detailLoading.value = false
  }
}

function updatePreview() {
  if (!hit.value) return
  previewURL.value = clipStreamURL(hit.value, selStart.value, selEnd.value, 'fast', false)
}

function selectAll() {
  sel.value = [0, Math.max(cues.value.length - 1, 0)]
  updatePreview()
}

function expandTo(i: number) {
  const [a, b] = sel.value
  if (i < a) sel.value = [i, b]
  else if (i > b) sel.value = [a, i]
  else sel.value = [i, i]
  updatePreview()
}

async function download() {
  if (!hit.value) return
  downloadPending.value = true
  clipError.value = ''
  const url = clipStreamURL(hit.value, selStart.value, selEnd.value, accurate.value ? 'accurate' : 'fast', true)
  try {
    const res = await fetch(url)
    if (res.status === 202) {
      const body = await res.json()
      jobProgress.value = 0
      jobLabel.value = 'Rendering accurate clip'
      await pollJob(body.jobId)
      window.location.href = url
    } else if (res.ok) {
      const blob = await res.blob()
      triggerBlobDownload(blob, filenameFromDisposition(res.headers.get('content-disposition')))
    } else {
      const body = await res.json().catch(() => ({}))
      clipError.value = body.error || `clip failed: ${res.statusText}`
    }
  } catch (err) {
    clipError.value = (err as Error).message
  } finally {
    downloadPending.value = false
    jobProgress.value = null
    jobLabel.value = ''
  }
}

async function pollJob(jobId: number) {
  for (let n = 0; n < 900; n++) {
    await new Promise((r) => setTimeout(r, 1000))
    try {
      const { jobs } = await api.jobs()
      const j = jobs?.find((x) => x.id === jobId)
      if (!j) continue
      jobProgress.value = j.progress || 0.02
      if (j.status === 'done') {
        jobProgress.value = 1
        return
      }
      if (j.status === 'error') throw new Error(j.error || 'render failed')
    } catch (err) {
      throw err
    }
  }
  throw new Error('render timed out')
}

function filenameFromDisposition(d: string | null): string {
  if (!d) return 'clip.mp4'
  const m = d.match(/filename="?([^";]+)"?/)
  return m ? m[1] : 'clip.mp4'
}

function triggerBlobDownload(blob: Blob, name: string) {
  const u = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = u
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(u), 30000)
}

async function copyLink() {
  if (!hit.value) return
  const url =
    window.location.origin +
    clipStreamURL(hit.value, selStart.value, selEnd.value, accurate.value ? 'accurate' : 'fast', false)
  try {
    await navigator.clipboard.writeText(url)
    window.dispatchEvent(
      new CustomEvent('app-toast', { detail: { text: 'Clip link copied to clipboard', color: 'success' } })
    )
  } catch {
    window.prompt('Copy this link', url)
  }
}

function fmt(s: number): string {
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = Math.floor(s % 60)
  return h > 0
    ? `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`
    : `${m}:${String(sec).padStart(2, '0')}`
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<style scoped>
.clip-dialog {
  background: #14141b;
  border: 1px solid rgba(255, 255, 255, 0.08);
}
.dialog-head {
  padding: 14px 18px;
  gap: 12px;
}
.min-width-0 {
  min-width: 0;
}
.preview-shell {
  border-radius: 14px;
  overflow: hidden;
  background: #000;
  border: 1px solid rgba(255, 255, 255, 0.07);
}
.preview {
  display: block;
  width: 100%;
  aspect-ratio: 16/9;
  background: #000;
}
.preview-empty {
  display: flex;
  align-items: center;
  justify-content: center;
}
.trim-block {
  margin-top: 14px;
}
.actions-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 4px;
}
.cues-panel {
  border: 1px solid rgba(255, 255, 255, 0.07);
  border-radius: 14px;
  padding: 12px;
  background: rgba(255, 255, 255, 0.02);
  height: 100%;
  display: flex;
  flex-direction: column;
}
.cue-scroll {
  overflow-y: auto;
  max-height: 420px;
  padding-right: 4px;
}
.cue-row {
  cursor: pointer;
  border-radius: 8px;
  margin-bottom: 2px;
}
.cue-row:hover {
  background: rgba(255, 255, 255, 0.045);
}
</style>
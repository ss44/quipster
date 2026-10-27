<template>
  <v-dialog
    v-model="open"
    max-width="980"
    scrollable
    transition="dialog-bottom-transition"
  >
    <v-card v-if="detail" :loading="detailLoading">
      <v-toolbar color="surface" flat>
        <v-toolbar-title class="text-subtitle-1">
          {{ detail.media.Title || detail.media.ShowTitle }}
          <span v-if="detail.media.ShowTitle && detail.media.Season" class="text-medium-emphasis">
            S{{ pad(detail.media.Season) }}E{{ pad(detail.media.Episode) }}
          </span>
          <span v-if="detail.media.Year" class="text-medium-emphasis"> ({{ detail.media.Year }})</span>
        </v-toolbar-title>
        <v-spacer />
        <v-btn icon="mdi-close" variant="text" @click="open = false" />
      </v-toolbar>

      <v-card-text class="pa-4">
        <v-row>
          <v-col cols="12" md="7">
            <video
              v-if="previewURL"
              :key="previewURL"
              :src="previewURL"
              controls
              autoplay
              class="preview rounded-lg"
            />
            <div v-else class="preview d-flex align-center justify-center bg-grey-darken-3 rounded-lg">
              <v-progress-circular indeterminate />
            </div>
            <div class="mt-3">
              <div class="text-caption text-medium-emphasis mb-1">
                Trim {{ fmt(selStart) }} – {{ fmt(selEnd) }} ({{ (selEnd - selStart).toFixed(1) }}s)
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
                @end="updatePreview"
              >
                <template #thumb-label="{ modelValue }">
                  {{ fmt(cues[Math.min(modelValue, cues.length - 1)]?.start ?? 0) }}
                </template>
              </v-range-slider>
            </div>
            <v-row class="mt-1" align="center">
              <v-col cols="auto">
                <v-switch
                  v-model="accurate"
                  color="primary"
                  hide-details
                  density="compact"
                  label="Re-encode (accurate cut)"
                />
              </v-col>
              <v-col cols="auto">
                <v-btn
                  color="primary"
                  prepend-icon="mdi-download"
                  :loading="downloading"
                  @click="download"
                >
                  Download
                </v-btn>
              </v-col>
              <v-col cols="auto">
                <v-btn
                  variant="tonal"
                  prepend-icon="mdi-link-variant"
                  @click="copyLink"
                >
                  Copy link
                </v-btn>
              </v-col>
            </v-row>
            <v-alert v-if="clipError" type="error" variant="tonal" density="compact" class="mt-2">
              {{ clipError }}
            </v-alert>
            <v-progress-linear
              v-if="jobProgress !== null"
              :model-value="Math.round(jobProgress * 100)"
              color="primary"
              height="6"
              class="mt-2 rounded"
            />
          </v-col>

          <v-col cols="12" md="5" class="cue-list">
            <div class="text-subtitle-2 text-medium-emphasis mb-2">Subtitles in selection</div>
            <v-list density="compact" class="bg-transparent">
              <v-list-item
                v-for="(c, i) in cues"
                :key="c.idx"
                :class="{ 'cue-active': i >= sel[0] && i <= sel[1], 'cue-before': i < sel[0], 'cue-after': i > sel[1] }"
                :subtitle="`${fmt(c.start)} → ${fmt(c.end)}`"
                @click="expandTo(i)"
              >
                <v-list-item-title class="text-body-2 text-wrap">{{ c.text }}</v-list-item-title>
              </v-list-item>
              <v-list-item v-for="c in detail.contextBefore" :key="'b' + c.idx" class="cue-outside">
                <v-list-item-title class="text-caption text-wrap">{{ c.text }}</v-list-item-title>
              </v-list-item>
              <v-list-item v-for="c in detail.contextAfter" :key="'a' + c.idx" class="cue-outside">
                <v-list-item-title class="text-caption text-wrap">{{ c.text }}</v-list-item-title>
              </v-list-item>
            </v-list>
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
const downloading = ref(false)
const clipError = ref('')
const jobProgress = ref<number | null>(null)
const previewURL = ref('')

const cues = computed(() => detail.value?.cues ?? [])
const selStart = computed(() => cues.value[Math.min(sel.value[0], cues.value.length - 1)]?.start ?? 0)
const selEnd = computed(() => cues.value[Math.min(sel.value[1], cues.value.length - 1)]?.end ?? 0)

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
  } catch (e) {
    clipError.value = (e as Error).message
  } finally {
    detailLoading.value = false
  }
}

function updatePreview() {
  if (!hit.value) return
  previewURL.value = clipStreamURL(hit.value, selStart.value, selEnd.value, 'fast', false)
}

function expandTo(i: number) {
  const [a, b] = sel.value
  if (i < a) sel.value = [i, b]
  else if (i > b) sel.value = [a, i]
  updatePreview()
}

async function download() {
  if (!hit.value) return
  downloading.value = true
  clipError.value = ''
  const url = clipStreamURL(hit.value, selStart.value, selEnd.value, accurate.value ? 'accurate' : 'fast', true)
  try {
    const res = await fetch(url)
    if (res.status === 202) {
      const body = await res.json()
      jobProgress.value = 0
      await pollJob(body.jobId, url)
      window.location.href = url
    } else if (res.ok) {
      const blob = await res.blob()
      triggerBlobDownload(blob, filenameFromDisposition(res.headers.get('content-disposition')))
    } else {
      clipError.value = `clip failed: ${res.statusText}`
    }
  } catch (e) {
    clipError.value = (e as Error).message
  } finally {
    downloading.value = false
    jobProgress.value = null
  }
}

async function pollJob(jobId: number, _url: string) {
  for (let n = 0; n < 600; n++) {
    await new Promise((r) => setTimeout(r, 1000))
    try {
      const { jobs } = await api.jobs()
      const j = jobs?.find((x) => x.id === jobId)
      if (!j) continue
      jobProgress.value = j.progress || 0.02
      if (j.status === 'done') return
      if (j.status === 'error') throw new Error(j.error || 'render failed')
    } catch (e) {
      if ((e as Error).message !== '') throw e
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
  } catch {
    window.prompt('Copy this link', url)
  }
}

function fmt(s: number): string {
  const m = Math.floor(s / 60)
  const sec = Math.floor(s % 60)
  return `${m}:${String(sec).padStart(2, '0')}`
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<style scoped>
.preview {
  width: 100%;
  aspect-ratio: 16/9;
  background: black;
}
.cue-active {
  background: rgba(124, 77, 255, 0.16);
  border-radius: 8px;
}
.cue-before,
.cue-after {
  opacity: 0.45;
}
.cue-outside {
  opacity: 0.3;
}
</style>
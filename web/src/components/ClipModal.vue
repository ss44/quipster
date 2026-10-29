<template>
  <v-dialog v-model="open" max-width="1120" scrollable transition="dialog-bottom-transition">
    <div v-if="detail" class="clip-panel">
      <header class="clip-head">
        <div class="clip-head-main">
          <p class="eyebrow">Clip studio</p>
          <h2 class="clip-title">{{ titleLine }}</h2>
          <p class="clip-path mono">{{ detail.media.path }}</p>
        </div>
        <button type="button" class="close-btn" aria-label="Close" @click="open = false">
          <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M3.5 3.5l9 9m0-9l-9 9" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
          </svg>
        </button>
      </header>

      <div class="clip-body">
        <section class="player-col">
          <div class="player">
            <video
              v-if="previewURL"
              :key="previewURL"
              :src="previewURL"
              :poster="posterURL"
              controls
              autoplay
              preload="auto"
              @loadeddata="onPreviewLoaded"
              @error="onPreviewError"
            />
            <div v-if="previewLoading" class="preview-overlay">
              <span class="spinner"></span>
              <span>Preparing preview…</span>
            </div>
          </div>

          <div class="trim">
            <div class="clip-section-head">
              <span class="section-label">Clip range</span>
              <span class="trim-meta">
                <span class="mono">{{ fmt(selStart) }} – {{ fmt(selEnd) }}</span>
                <span class="sep">·</span>
                {{ selectedCount }} {{ selectedCount === 1 ? 'line' : 'lines' }}
              </span>
            </div>
            <v-range-slider
              v-model="sel"
              :min="0"
              :max="Math.max(cues.length - 1, 0)"
              :step="1"
              hide-details
              density="compact"
              track-color="rgba(255,255,255,0.10)"
              color="primary"
              class="trim-slider"
              @end="updatePreview"
            />
            <div class="trim-hint">
              <span>Drag to include more or fewer subtitle lines.</span>
              <button type="button" class="link-btn" @click="selectAll">Select all</button>
            </div>
          </div>

          <div class="actions">
            <div class="mode">
              <span class="field-label">Output</span>
              <v-btn-toggle
                :model-value="mode"
                mandatory
                density="compact"
                divided
                variant="outlined"
                color="primary"
                class="mode-toggle"
                @update:model-value="setMode"
              >
                <v-btn value="fast" size="small">
                  <span class="mode-btn">Fast cut</span>
                </v-btn>
                <v-btn value="accurate" size="small">
                  <span class="mode-btn">Precise</span>
                </v-btn>
              </v-btn-toggle>
            </div>
            <div class="buttons">
              <button type="button" class="btn-secondary" :disabled="downloading" @click="copyLink">
                Copy link
              </button>
              <button type="button" class="btn-primary" :disabled="downloading" @click="download">
                {{ downloading ? 'Working…' : 'Download' }}
              </button>
            </div>
          </div>

          <p class="hint">
            Fast copies the original stream from the previous keyframe. Precise re-encodes for exact
            timing. Preview is always a lightweight transcode.
          </p>

          <p v-if="clipError" class="error-msg">{{ clipError }}</p>
          <p v-if="previewFailed" class="error-msg">
            Preview could not be played in this browser. Try downloading instead.
          </p>

          <div v-if="jobProgress !== null" class="progress">
            <div class="progress-labels">
              <span>{{ jobLabel || 'Rendering clip…' }}</span>
              <span>{{ Math.round((jobProgress || 0) * 100) }}%</span>
            </div>
            <v-progress-linear
              :model-value="Math.round((jobProgress || 0) * 100)"
              color="primary"
              height="4"
              rounded
              :indeterminate="jobProgress === 0"
            />
          </div>
        </section>

        <aside class="transcript">
          <header class="transcript-head">
            <span class="panel-title">Transcript</span>
            <span class="mono muted">{{ selectedCount }}/{{ cues.length }}</span>
          </header>
          <div class="scroll">
            <div v-for="c in detail.contextBefore" :key="'b' + c.idx" class="line is-context">
              <span class="line-time">{{ fmt(c.start) }}</span>
              <span class="line-text">{{ c.text }}</span>
            </div>
            <button
              v-for="(c, i) in cues"
              :key="c.idx"
              type="button"
              class="line"
              :class="{ active: inRange(i), dim: !inRange(i) }"
              @click="expandTo(i)"
            >
              <span class="line-time">{{ fmt(c.start) }}</span>
              <span class="line-text">{{ c.text }}</span>
            </button>
            <div v-for="c in detail.contextAfter" :key="'a' + c.idx" class="line is-context">
              <span class="line-time">{{ fmt(c.start) }}</span>
              <span class="line-text">{{ c.text }}</span>
            </div>
          </div>
        </aside>
      </div>
    </div>
  </v-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, clipStreamURL, type ChunkDetail, type Hit } from '../api'

const open = ref(false)
const detail = ref<ChunkDetail | null>(null)
const hit = ref<Hit | null>(null)
const sel = ref<[number, number]>([0, 0])
const accurate = ref(false)
const downloadPending = ref(false)
const clipError = ref('')
const jobProgress = ref<number | null>(null)
const jobLabel = ref('')
const previewURL = ref('')
const previewLoading = ref(false)
const previewFailed = ref(false)
const posterURL = ref('')

// MediaError codes from the HTMLMediaElement spec. Only MEDIA_ERR_ABORTED is
// recoverable: it is reported when a media load is cancelled, which happens
// routinely here because the trim slider swaps the preview URL while the
// previous load is still in flight.
const MEDIA_ERR_ABORTED = 1

const cues = computed(() => detail.value?.cues ?? [])
const selStart = computed(() => cues.value[Math.min(sel.value[0], cues.value.length - 1)]?.start ?? 0)
const selEnd = computed(() => cues.value[Math.min(sel.value[1], cues.value.length - 1)]?.end ?? 0)
const selectedCount = computed(() => Math.max(0, sel.value[1] - sel.value[0] + 1))
const downloading = computed(() => downloadPending.value)
const mode = computed({
  get: () => (accurate.value ? 'accurate' : 'fast'),
  set: (value: string) => {
    accurate.value = value === 'accurate'
    updatePreview()
  },
})
const titleLine = computed(() => {
  if (!detail.value) return ''
  const media = detail.value.media
  const parts = [media.showTitle || media.title]
  if (media.showTitle && media.season) parts.push(`S${pad(media.season)}E${pad(media.episode)}`)
  if (media.year) parts.push(`(${media.year})`)
  return parts.join(' ')
})

onMounted(() => window.addEventListener('open-clip', onOpenClip as EventListener))
onUnmounted(() => window.removeEventListener('open-clip', onOpenClip as EventListener))

function setMode(value: unknown) {
  mode.value = String(value)
}

function inRange(index: number): boolean {
  return index >= sel.value[0] && index <= sel.value[1]
}

async function onOpenClip(e: Event) {
  const next = (e as CustomEvent).detail as Hit
  hit.value = next
  open.value = true
  clipError.value = ''
  previewFailed.value = false
  jobProgress.value = null
  try {
    detail.value = await api.chunk(next.chunkId)
    sel.value = [0, Math.max(cues.value.length - 1, 0)]
    updatePreview()
  } catch (err) {
    clipError.value = (err as Error).message
  }
}

function updatePreview() {
  if (!hit.value || cues.value.length === 0) return
  previewLoading.value = true
  posterURL.value = `/api/thumb/${hit.value.mediaId}?t=${selStart.value}&w=640`
  previewURL.value = clipStreamURL(hit.value, selStart.value, selEnd.value, 'preview', false)
}

function onPreviewLoaded() {
  previewLoading.value = false
  // A successful load clears or prevents the warning: an earlier failure (for
  // example an aborted request while the trim range was adjusted) must not
  // outlive a preview that now plays correctly.
  previewFailed.value = false
}

function onPreviewError(e: Event) {
  const el = e.target as HTMLVideoElement
  // Ignore errors from a preview that has since been replaced. When the trim
  // range changes, the discarded element's cancelled load emits an error that
  // no longer describes the visible preview, and reacting to it would flag a
  // preview that is about to load fine.
  if (el.getAttribute('src') !== previewURL.value) return
  previewLoading.value = false
  // An aborted load is expected while trimming and recovers on its own with
  // the next preview; only genuine failures (unsupported format, decode or
  // network errors) warrant telling the user playback cannot be recovered.
  if (el.error && el.error.code === MEDIA_ERR_ABORTED) return
  previewFailed.value = true
}

function selectAll() {
  sel.value = [0, Math.max(cues.value.length - 1, 0)]
  updatePreview()
}

function expandTo(index: number) {
  const [a, b] = sel.value
  if (index < a) sel.value = [index, b]
  else if (index > b) sel.value = [a, index]
  else sel.value = [index, index]
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
      jobLabel.value = 'Rendering accurate clip…'
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
    await new Promise((resolve) => setTimeout(resolve, 1000))
    const { jobs } = await api.jobs()
    const job = jobs?.find((item) => item.id === jobId)
    if (!job) continue
    jobProgress.value = job.progress || 0.02
    if (job.status === 'done') {
      jobProgress.value = 1
      return
    }
    if (job.status === 'error') throw new Error(job.error || 'render failed')
  }
  throw new Error('render timed out')
}

function filenameFromDisposition(value: string | null): string {
  if (!value) return 'clip.mp4'
  const match = value.match(/filename="?([^";]+)"?/)
  return match ? match[1] : 'clip.mp4'
}

function triggerBlobDownload(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 30000)
}

async function copyLink() {
  if (!hit.value) return
  const url =
    window.location.origin +
    clipStreamURL(hit.value, selStart.value, selEnd.value, accurate.value ? 'accurate' : 'fast', false)
  try {
    await navigator.clipboard.writeText(url)
    window.dispatchEvent(
      new CustomEvent('app-toast', { detail: { text: 'Clip link copied', color: 'success' } }),
    )
  } catch {
    window.prompt('Copy this link', url)
  }
}

function fmt(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}
</script>

<style scoped>
.clip-panel {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 16px;
  overflow: hidden;
}
.clip-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 20px 22px 16px;
  border-bottom: 1px solid var(--border);
}
.clip-head-main {
  min-width: 0;
}
.clip-title {
  font-size: 19px;
  font-weight: 650;
  letter-spacing: -0.01em;
  margin: 4px 0 4px;
}
.clip-path {
  color: var(--faint);
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.close-btn {
  flex: none;
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  color: var(--muted);
  border: 1px solid var(--border);
  border-radius: 8px;
  background: transparent;
}
.close-btn:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.05);
}
.clip-body {
  display: grid;
  grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr);
  gap: 22px;
  padding: 20px 22px 22px;
}
.player-col {
  min-width: 0;
}
.player {
  position: relative;
  border-radius: 12px;
  overflow: hidden;
  background: #050608;
  border: 1px solid var(--border);
  aspect-ratio: 16 / 9;
}
.player video {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
  background: #000;
}
.preview-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  background: rgba(6, 7, 9, 0.6);
  font-size: 12px;
  color: var(--muted);
  pointer-events: none;
}
.spinner {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  border: 2px solid rgba(255, 255, 255, 0.15);
  border-top-color: var(--accent);
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
.trim {
  margin-top: 16px;
}
.clip-section-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 2px;
}
.section-label,
.field-label {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--faint);
}
.trim-meta {
  font-size: 12px;
  color: var(--muted);
}
.trim-meta .mono {
  color: var(--text);
}
.trim-meta .sep {
  padding: 0 2px;
  opacity: 0.6;
}
.trim-slider {
  margin: 2px 0 -6px;
}
.trim-hint {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11.5px;
  color: var(--faint);
}
.link-btn {
  background: none;
  border: none;
  padding: 0;
  font-size: 11.5px;
  color: var(--muted);
  text-decoration: underline;
  text-underline-offset: 3px;
}
.link-btn:hover {
  color: var(--text);
}
.actions {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-top: 18px;
  flex-wrap: wrap;
}
.mode {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.mode-btn {
  font-size: 12px;
}
.buttons {
  display: flex;
  align-items: center;
  gap: 8px;
}
.btn-primary {
  padding: 9px 16px;
  border-radius: 9px;
  border: none;
  background: var(--accent);
  color: #151515;
  font-size: 13px;
  font-weight: 650;
}
.btn-primary:hover {
  background: var(--accent-strong);
}
.btn-primary:disabled {
  opacity: 0.6;
  cursor: default;
}
.btn-secondary {
  padding: 9px 14px;
  border-radius: 9px;
  border: 1px solid var(--border-strong);
  background: transparent;
  color: var(--text);
  font-size: 13px;
}
.btn-secondary:hover {
  background: rgba(255, 255, 255, 0.05);
}
.hint {
  margin: 12px 0 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--faint);
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
.progress {
  margin-top: 12px;
}
.progress-labels {
  display: flex;
  justify-content: space-between;
  font-size: 11.5px;
  color: var(--muted);
  margin-bottom: 4px;
}
.transcript {
  display: flex;
  flex-direction: column;
  min-height: 0;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.015);
  overflow: hidden;
}
.transcript-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 11px 14px;
  border-bottom: 1px solid var(--border);
}
.panel-title {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--faint);
}
.transcript-head .mono {
  font-size: 11px;
}
.scroll {
  overflow-y: auto;
  max-height: 420px;
  padding: 8px;
}
.line {
  display: grid;
  grid-template-columns: 54px 1fr;
  gap: 10px;
  width: 100%;
  text-align: left;
  padding: 7px 8px;
  border: none;
  border-left: 2px solid transparent;
  border-radius: 6px;
  background: transparent;
  cursor: pointer;
}
.line:hover {
  background: rgba(255, 255, 255, 0.04);
}
.line.active {
  background: rgba(255, 209, 102, 0.06);
  border-left-color: var(--accent);
}
.line.dim {
  opacity: 0.5;
}
.line.is-context {
  opacity: 0.32;
  cursor: default;
}
.line-time {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--faint);
  padding-top: 2px;
}
.line-text {
  font-size: 12.5px;
  line-height: 1.5;
  color: #d9dce1;
}
@media (max-width: 900px) {
  .clip-body {
    grid-template-columns: 1fr;
  }
  .scroll {
    max-height: 240px;
  }
}
</style>
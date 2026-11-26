<template>
  <v-dialog v-model="open" max-width="1120" scrollable transition="dialog-bottom-transition">
    <div class="clip-panel">
      <header class="clip-head">
        <div class="clip-head-main">
          <p class="eyebrow">Clip studio</p>
          <h2 class="clip-title">
            <HazeText v-if="detailLoading" label="Loading clip title" text="Film and episode" />
            <template v-else>{{ detail ? titleLine : 'Clip unavailable' }}</template>
          </h2>
          <p v-if="detail" class="clip-path mono">{{ detail.media.path }}</p>
        </div>
        <button type="button" class="close-btn" aria-label="Close" @click="open = false">
          <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M3.5 3.5l9 9m0-9l-9 9" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
          </svg>
        </button>
      </header>

      <div v-if="detailLoading" class="clip-body">
        <div class="player-col">
          <div class="player">
            <div class="preview-overlay"><HazeText label="Loading clip" text="Preparing your scene" /></div>
          </div>
        </div>
        <aside class="transcript">
          <header class="transcript-head"><span class="panel-title">Transcript</span></header>
          <div class="scroll">
            <p v-for="i in 4" :key="i" class="line line-pending">
              <HazeText label="Loading subtitle line" text="Dialogue from this moment" />
            </p>
          </div>
        </aside>
      </div>
      <p v-else-if="!detail" class="error-msg clip-load-error" role="alert">{{ clipError }}</p>
      <div v-else class="clip-body">
        <section class="player-col">
          <div ref="playerRef" class="player">
            <video
              v-if="previewURL"
              ref="videoRef"
              :key="previewURL"
              :src="previewURL"
              :poster="posterURL"
              :controls="!previewLoading"
              autoplay
              preload="auto"
              @loadeddata="onPreviewLoaded"
              @loadedmetadata="onVideoMetrics"
              @timeupdate="onVideoTimeUpdate"
              @seeked="onVideoTimeUpdate"
              @error="onPreviewError"
            />
            <SubtitleOverlay
              v-if="cues.length > 0"
              :style-config="subtitleStyle"
              :cues="activeCues"
              :clip-start="clipStartSec"
              :current-time="videoCurrentTime"
              :video-width="videoWidth"
              :video-height="videoHeight"
              :container-width="playerWidth"
              :container-height="playerHeight"
            />
            <div v-if="previewLoading" class="preview-overlay">
              <HazeText label="Preparing preview" text="Preparing preview" />
            </div>
          </div>

          <div class="trim">
            <div class="clip-section-head">
              <div class="range-heading">
                <span class="section-label">Clip range</span>
                <button type="button" class="range-extend" aria-label="Include previous subtitle line"
                  title="Include previous subtitle line" :disabled="!canExtendBefore || extending" @click="extendLine(-1)">
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M7 3L2 8l5 5M2 8h12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                </button>
                <button type="button" class="range-extend" aria-label="Include next subtitle line"
                  title="Include next subtitle line" :disabled="!canExtendAfter || extending" @click="extendLine(1)">
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M9 3l5 5-5 5M14 8H2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                </button>
              </div>
              <span class="trim-meta">
                <span class="mono">{{ trimRangeLabel }}</span>
                <span class="sep">·</span>
                <span class="mono">{{ clipSpanSec.toFixed(1) }}s</span>
                <span class="sep">·</span>
                {{ activeCueCount }} {{ activeCueCount === 1 ? 'line' : 'lines' }}
              </span>
            </div>
            <v-range-slider
              v-model="sel"
              :min="0"
              :max="Math.max(cues.length - 1, 0)"
              :step="1"
              :disabled="cues.length === 0"
              hide-details
              density="compact"
              track-color="rgba(255,255,255,0.10)"
              color="primary"
              class="trim-slider"
              @end="updatePreview"
            />
            <div class="trim-hint">
              <span>Drag to include subtitle lines, or fine-tune start &amp; end below.</span>
              <button type="button" class="link-btn" :disabled="cues.length === 0" @click="selectAll">Select all</button>
            </div>
            <ClipTimingControls
              :start="activeBounds.start"
              :end="activeBounds.end"
              :duration-sec="mediaDurationSec"
              :max-clip-sec="maxClipSec"
              :playhead-sec="playheadSec"
              :is-custom="isCustomRange"
              :disabled="cues.length === 0"
              @adjust-start="onAdjustStart"
              @adjust-end="onAdjustEnd"
              @reset="resetCustomBounds"
            />
          </div>

          <div class="actions">
            <div class="mode">
              <span class="field-label">Output</span>
              <v-btn-toggle
                :model-value="outputMode"
                mandatory
                density="compact"
                divided
                variant="outlined"
                color="primary"
                class="mode-toggle"
                @update:model-value="setMode"
              >
                <v-btn value="fast" size="small" :disabled="subtitleStyle.enabled">
                  <span class="mode-btn">Fast cut</span>
                </v-btn>
                <v-btn value="accurate" size="small">
                  <span class="mode-btn">Precise</span>
                </v-btn>
                <v-btn value="gif" size="small">
                  <span class="mode-btn">GIF</span>
                </v-btn>
              </v-btn-toggle>
            </div>
            <div class="buttons">
              <button type="button" class="btn-secondary" :disabled="submittingExport || cues.length === 0 || exportBlocked" @click="copyLink">
                Copy link
              </button>
              <button type="button" class="btn-primary" :disabled="downloading || cues.length === 0 || exportBlocked" @click="download">
                {{ downloadButtonLabel }}
              </button>
            </div>
          </div>

          <p class="hint">
            <template v-if="outputMode === 'gif'">
              GIF exports a looping, audio-free animation ({{ gifResLabel }}, {{ gifFps }} fps, max
              {{ formatLimitSec(maxGifSec) }}) sized for sharing on social apps.
              <template v-if="subtitleStyle.enabled">
                Burned-in subtitles will be rendered directly into the GIF.
              </template>
            </template>
            <template v-else-if="subtitleStyle.enabled">
              Burned-in subtitles require re-encoding (Precise or GIF), so Fast cut is unavailable while subtitles are enabled.
            </template>
            <template v-else>
              Fast copies the original stream from the previous keyframe. Precise re-encodes for exact
              timing. Preview is always a lightweight transcode.
            </template>
          </p>

          <p v-if="clipTooLong" class="limit-msg" role="status">
            Clips cannot exceed {{ formatLimitSec(maxClipSec) }} (selected clip is
            {{ clipSpanSec.toFixed(1) }}s{{ isCustomRange ? '' : ' including padding' }}). Trim the range before exporting.
          </p>
          <p v-else-if="gifTooLong" class="limit-msg" role="status">
            GIF exports cannot exceed {{ formatLimitSec(maxGifSec) }} (selected clip is
            {{ clipSpanSec.toFixed(1) }}s{{ isCustomRange ? '' : ' including padding' }}). Trim the range or switch to Fast cut / Precise.
          </p>

          <p v-if="clipError" class="error-msg">{{ clipError }}</p>
          <p v-if="previewFailed" class="error-msg">
            Preview could not be played in this browser. Try downloading instead.
          </p>

          <div v-if="activeClipExport" class="progress">
            <div class="progress-labels">
              <span>{{ activeExportLabel }}</span>
              <span>{{ Math.round((activeClipExport.progress || 0) * 100) }}%</span>
            </div>
            <v-progress-linear
              :model-value="Math.round((activeClipExport.progress || 0) * 100)"
              color="primary"
              height="4"
              rounded
              :indeterminate="activeClipExport.status === 'queued' || activeClipExport.progress <= 0.02"
            />
            <p class="progress-note">Rendering in background — safe to close this preview or browse away.</p>
          </div>
        </section>

        <div class="side-col">
          <aside class="transcript" :class="{ collapsed: sideTab !== 'transcript' }">
            <header class="transcript-head">
              <button
                type="button"
                class="transcript-toggle"
                :aria-expanded="sideTab === 'transcript'"
                aria-controls="clip-transcript-body"
                @click="toggleTranscript"
              >
                <span class="transcript-toggle-left">
                  <svg
                    v-if="cues.length > 0"
                    class="chevron"
                    :class="{ rotated: sideTab === 'transcript' }"
                    width="14"
                    height="14"
                    viewBox="0 0 16 16"
                    aria-hidden="true"
                  >
                    <path d="M4.5 6l3.5 4 3.5-4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                  <span class="panel-title">Transcript</span>
                </span>
                <span class="mono muted">{{ activeCueCount }}/{{ cues.length }}</span>
              </button>
            </header>
            <div v-if="sideTab === 'transcript'" id="clip-transcript-body" class="scroll">
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
            </div>
          </aside>

          <SubtitleStylePanel
            v-if="cues.length > 0"
            v-model="subtitleStyle"
            :expanded="sideTab === 'subtitles'"
            @update:expanded="sideTab = $event ? 'subtitles' : 'transcript'"
          />
        </div>
      </div>
    </div>
  </v-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, clipQueryParams, clipStreamURL, type ChunkDetail, type CueDTO, type Hit } from '../api'
import { useClipExports } from '../lib/clipDownloads'
import {
  formatTimestamp,
  overlappingCues,
  roundMs,
  setClipEnd,
  setClipStart,
  transcriptClipBounds,
  type ClipBounds,
} from '../lib/clipBounds'
import {
  ensureFontFacesLoaded,
  loadSubtitleStyle,
  saveSubtitleStyle,
  type SubtitleStyle,
} from '../lib/subtitles'
import HazeText from './HazeText.vue'
import ClipTimingControls from './clip/ClipTimingControls.vue'
import SubtitleOverlay from './clip/SubtitleOverlay.vue'
import SubtitleStylePanel from './clip/SubtitleStylePanel.vue'

type OutputMode = 'fast' | 'accurate' | 'gif'
type SideTab = 'transcript' | 'subtitles'

const { startClipExport, findExportByQuery } = useClipExports()

const open = ref(false)
const detail = ref<ChunkDetail | null>(null)
const detailLoading = ref(false)
const hit = ref<Hit | null>(null)
const sel = ref<[number, number]>([0, 0])
const customBounds = ref<ClipBounds | null>(null)
const sideTab = ref<SideTab>('transcript')
const subtitleStyle = ref<SubtitleStyle>(loadSubtitleStyle())
const outputMode = ref<OutputMode>(subtitleStyle.value.enabled ? 'accurate' : 'fast')
const submittingExport = ref(false)
const clipError = ref('')
const previewURL = ref('')
const previewLoading = ref(false)
const previewFailed = ref(false)
const posterURL = ref('')
const playerRef = ref<HTMLElement | null>(null)
const videoRef = ref<HTMLVideoElement | null>(null)
const videoCurrentTime = ref(0)
const videoWidth = ref(0)
const videoHeight = ref(0)
const playerWidth = ref(0)
const playerHeight = ref(0)
let playerObserver: ResizeObserver | null = null
let previewDebounceTimer: ReturnType<typeof setTimeout> | null = null
const PREVIEW_DEBOUNCE_MS = 250

// MediaError codes from the HTMLMediaElement spec. Only MEDIA_ERR_ABORTED is
// recoverable: it is reported when a media load is cancelled, which happens
// routinely here because the trim slider swaps the preview URL while the
// previous load is still in flight.
const MEDIA_ERR_ABORTED = 1

const cues = ref<CueDTO[]>([])
const extending = ref(false)
const exhaustedBefore = ref(false)
const exhaustedAfter = ref(false)
let session = 0
let selectionVersion = 0
const canExtendBefore = computed(() => cues.value.length > 0 && (
  sel.value[0] > 0 || (!exhaustedBefore.value && cues.value[0].idx > 0)
))
const canExtendAfter = computed(() => cues.value.length > 0 && (
  sel.value[1] < cues.value.length - 1 || !exhaustedAfter.value
))
const selStart = computed(() => cues.value[Math.min(sel.value[0], cues.value.length - 1)]?.start ?? 0)
const selEnd = computed(() => cues.value[Math.min(sel.value[1], cues.value.length - 1)]?.end ?? 0)
const selectedCues = computed(() => cues.value.length ? cues.value.slice(sel.value[0], sel.value[1] + 1) : [])
const defaultPadMs = computed(() => detail.value?.clipLimits?.defaultPadMs ?? 250)
const mediaDurationSec = computed(() => detail.value?.media.durationSec ?? 0)
const maxClipSec = computed(() => detail.value?.clipLimits?.maxClipSec ?? 120)
const transcriptBounds = computed<ClipBounds>(() =>
  cues.value.length
    ? transcriptClipBounds(selStart.value, selEnd.value, defaultPadMs.value, mediaDurationSec.value)
    : { start: 0, end: 0 },
)
const activeBounds = computed<ClipBounds>(() => customBounds.value ?? transcriptBounds.value)
const isCustomRange = computed(() => customBounds.value !== null)
const requestStart = computed(() => (customBounds.value ? customBounds.value.start : selStart.value))
const requestEnd = computed(() => (customBounds.value ? customBounds.value.end : selEnd.value))
const requestPadMs = computed(() => (customBounds.value ? 0 : defaultPadMs.value))
const activeCues = computed(() => {
  if (!cues.value.length) return []
  if (customBounds.value) {
    return overlappingCues(cues.value, customBounds.value.start, customBounds.value.end)
  }
  return selectedCues.value
})
const activeCueIdxSet = computed(() => new Set(activeCues.value.map((cue) => cue.idx)))
const activeCueCount = computed(() => activeCues.value.length)
const clipStartSec = computed(() =>
  customBounds.value ? customBounds.value.start : Math.max(0, selStart.value - defaultPadMs.value / 1000),
)
const maxGifSec = computed(() => detail.value?.clipLimits?.maxGifSec ?? 10)
const gifFps = computed(() => detail.value?.clipLimits?.gifFps ?? 12)
const gifResLabel = computed(() => formatGifRes(detail.value?.clipLimits?.gifRes ?? '480:-2'))
const clipSpanSec = computed(() => {
  if (!cues.value.length) return 0
  if (customBounds.value) {
    return Math.max(0, roundMs(customBounds.value.end - customBounds.value.start))
  }
  return Math.max(0, selEnd.value - selStart.value) + (2 * defaultPadMs.value) / 1000
})
const clipTooLong = computed(() =>
  cues.value.length > 0 && clipSpanSec.value > maxClipSec.value + 0.001,
)
const gifTooLong = computed(() =>
  outputMode.value === 'gif' && cues.value.length > 0 && clipSpanSec.value > maxGifSec.value + 0.001,
)
const exportBlocked = computed(() => clipTooLong.value || gifTooLong.value)
const trimRangeLabel = computed(() =>
  customBounds.value
    ? `${formatTimestamp(customBounds.value.start)} – ${formatTimestamp(customBounds.value.end)}`
    : `${fmt(selStart.value)} – ${fmt(selEnd.value)}`,
)
const playheadSec = computed<number | null>(() => {
  if (!previewURL.value || previewLoading.value || previewFailed.value) return null
  return roundMs(clipStartSec.value + videoCurrentTime.value)
})
const currentExportQuery = computed(() => {
  if (!hit.value || !cues.value.length) return ''
  return clipQueryParams(
    hit.value,
    requestStart.value,
    requestEnd.value,
    outputMode.value,
    requestPadMs.value,
    subtitleStyle.value,
  ).toString()
})
const currentClipExport = computed(() =>
  currentExportQuery.value ? findExportByQuery(currentExportQuery.value) : undefined,
)
const activeClipExport = computed(() => {
  const item = currentClipExport.value
  return item && (item.status === 'queued' || item.status === 'running') ? item : null
})
const activeExportLabel = computed(() => {
  if (!activeClipExport.value) return ''
  if (activeClipExport.value.status === 'queued') return 'Queued for background render…'
  if (activeClipExport.value.mode === 'gif') return 'Rendering GIF…'
  if (activeClipExport.value.mode === 'fast') return 'Preparing clip…'
  return 'Rendering precise clip…'
})
const downloading = computed(() => submittingExport.value || activeClipExport.value !== null)
const downloadButtonLabel = computed(() => {
  if (submittingExport.value) return 'Starting…'
  if (activeClipExport.value) return 'Rendering…'
  return outputMode.value === 'gif' ? 'Download GIF' : 'Download'
})
const titleLine = computed(() => {
  if (!detail.value) return ''
  const media = detail.value.media
  const parts = [media.showTitle || media.title]
  if (media.showTitle && media.season) parts.push(`S${pad(media.season)}E${pad(media.episode)}`)
  if (media.year) parts.push(`(${media.year})`)
  return parts.join(' ')
})

onMounted(() => {
  ensureFontFacesLoaded()
  window.addEventListener('open-clip', onOpenClip as EventListener)
})
onUnmounted(() => {
  session++
  clearPreviewDebounce()
  playerObserver?.disconnect()
  playerObserver = null
  window.removeEventListener('open-clip', onOpenClip as EventListener)
})
watch(open, value => {
  if (!value) {
    session++
    clearPreviewDebounce()
  }
}, { flush: 'sync' })
watch(sel, () => {
  selectionVersion++
  customBounds.value = null
}, { deep: true, flush: 'sync' })
watch(subtitleStyle, (next) => {
  saveSubtitleStyle(next)
  if (next.enabled && outputMode.value === 'fast') {
    outputMode.value = 'accurate'
  }
}, { deep: true })
watch(playerRef, (el) => {
  playerObserver?.disconnect()
  playerObserver = null
  if (!el) return
  const sync = () => {
    playerWidth.value = el.clientWidth
    playerHeight.value = el.clientHeight
  }
  sync()
  if (typeof ResizeObserver !== 'undefined') {
    playerObserver = new ResizeObserver(sync)
    playerObserver.observe(el)
  }
})

function toggleTranscript() {
  if (sideTab.value === 'transcript') {
    if (cues.value.length > 0) sideTab.value = 'subtitles'
    return
  }
  sideTab.value = 'transcript'
}

function setMode(value: unknown) {
  if (value === 'fast' && subtitleStyle.value.enabled) return
  if (value === 'fast' || value === 'accurate' || value === 'gif') {
    outputMode.value = value
  }
}

function inRange(index: number): boolean {
  if (customBounds.value) {
    const cue = cues.value[index]
    return cue !== undefined && activeCueIdxSet.value.has(cue.idx)
  }
  return index >= sel.value[0] && index <= sel.value[1]
}

function clearPreviewDebounce() {
  if (previewDebounceTimer !== null) {
    clearTimeout(previewDebounceTimer)
    previewDebounceTimer = null
  }
}

function schedulePreviewUpdate() {
  clearPreviewDebounce()
  previewDebounceTimer = setTimeout(() => {
    previewDebounceTimer = null
    updatePreview()
  }, PREVIEW_DEBOUNCE_MS)
}

async function onOpenClip(e: Event) {
  const requestSession = ++session
  clearPreviewDebounce()
  const next = (e as CustomEvent).detail as Hit
  hit.value = next
  open.value = true
  detail.value = null
  detailLoading.value = true
  extending.value = false
  exhaustedBefore.value = false
  exhaustedAfter.value = false
  customBounds.value = null
  cues.value = []
  previewURL.value = ''
  posterURL.value = ''
  previewLoading.value = false
  clipError.value = ''
  previewFailed.value = false
  sideTab.value = 'transcript'
  videoCurrentTime.value = 0
  videoWidth.value = 0
  videoHeight.value = 0
  if (subtitleStyle.value.enabled && outputMode.value === 'fast') {
    outputMode.value = 'accurate'
  }
  try {
    const result = await api.chunk(next.chunkId)
    if (requestSession !== session || !open.value) return
    detail.value = result
    videoWidth.value = result.media.width || 0
    videoHeight.value = result.media.height || 0
    const scene = detail.value.cues
    cues.value = scene.length ? [...new Map([
      ...detail.value.contextBefore, ...scene, ...detail.value.contextAfter,
    ].map(cue => [cue.idx, cue])).values()].sort((a, b) => a.idx - b.idx) : []
    sel.value = scene.length ? [
      cues.value.findIndex(cue => cue.idx === scene[0].idx),
      cues.value.findIndex(cue => cue.idx === scene[scene.length - 1].idx),
    ] : [0, 0]
    customBounds.value = null
    updatePreview()
  } catch (err) {
    if (requestSession !== session || !open.value) return
    clipError.value = (err as Error).message
  } finally {
    if (requestSession === session) detailLoading.value = false
  }
}

function updatePreview() {
  clearPreviewDebounce()
  if (!hit.value || cues.value.length === 0 || clipTooLong.value) return
  previewLoading.value = true
  videoCurrentTime.value = 0
  posterURL.value = `/api/thumb/${hit.value.mediaId}?t=${requestStart.value}&w=640`
  previewURL.value = clipStreamURL(
    hit.value,
    requestStart.value,
    requestEnd.value,
    'preview',
    false,
    requestPadMs.value,
  )
}

function onAdjustStart(targetStart: number, immediate: boolean) {
  if (!cues.value.length) return
  const base = customBounds.value ?? transcriptBounds.value
  const next = setClipStart(base, targetStart, {
    durationSec: mediaDurationSec.value,
    maxClipSec: maxClipSec.value,
  })
  if (customBounds.value && next.start === customBounds.value.start && next.end === customBounds.value.end) {
    return
  }
  customBounds.value = next
  selectionVersion++
  if (immediate) updatePreview()
  else schedulePreviewUpdate()
}

function onAdjustEnd(targetEnd: number, immediate: boolean) {
  if (!cues.value.length) return
  const base = customBounds.value ?? transcriptBounds.value
  const next = setClipEnd(base, targetEnd, {
    durationSec: mediaDurationSec.value,
    maxClipSec: maxClipSec.value,
  })
  if (customBounds.value && next.start === customBounds.value.start && next.end === customBounds.value.end) {
    return
  }
  customBounds.value = next
  selectionVersion++
  if (immediate) updatePreview()
  else schedulePreviewUpdate()
}

function resetCustomBounds() {
  if (!customBounds.value) return
  customBounds.value = null
  selectionVersion++
  updatePreview()
}

function onVideoMetrics(e: Event) {
  const el = e.target as HTMLVideoElement
  if (el.videoWidth > 0 && el.videoHeight > 0) {
    videoWidth.value = el.videoWidth
    videoHeight.value = el.videoHeight
  }
}

function onVideoTimeUpdate(e: Event) {
  const el = e.target as HTMLVideoElement
  videoCurrentTime.value = Number.isFinite(el.currentTime) ? el.currentTime : 0
}

function onPreviewLoaded(e: Event) {
  onVideoMetrics(e)
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

async function extendLine(direction: -1 | 1) {
  if (!hit.value || extending.value || !(direction === -1 ? canExtendBefore.value : canExtendAfter.value)) return
  const [start, end] = sel.value
  if (direction === -1 && start > 0) sel.value = [start - 1, end]
  else if (direction === 1 && end < cues.value.length - 1) sel.value = [start, end + 1]
  else {
    const idx = direction === -1 ? cues.value[0].idx - 1 : cues.value[cues.value.length - 1].idx + 1
    const requestSession = session
    const requestSelection = selectionVersion
    const isCurrent = () => requestSession === session && requestSelection === selectionVersion && open.value
    extending.value = true
    clipError.value = ''
    try {
      const result = await api.mediaCues(hit.value.mediaId, idx, idx)
      if (!isCurrent()) return
      if (!result.cues.length) {
        if (direction === -1) exhaustedBefore.value = true
        else exhaustedAfter.value = true
        return
      }
      // Prepending shifts the old scene right by one; keeping the start index
      // includes exactly the new neighbor without dropping any selected lines.
      cues.value = direction === -1 ? [...result.cues, ...cues.value] : [...cues.value, ...result.cues]
      sel.value = [start, end + 1]
    } catch (err) {
      if (!isCurrent()) return
      clipError.value = (err as Error).message
    } finally {
      if (requestSession === session) extending.value = false
    }
  }
  updatePreview()
}

function selectAll() {
  customBounds.value = null
  sel.value = [0, Math.max(cues.value.length - 1, 0)]
  updatePreview()
}

function expandTo(index: number) {
  customBounds.value = null
  const [a, b] = sel.value
  if (index < a) sel.value = [index, b]
  else if (index > b) sel.value = [a, index]
  else sel.value = [index, index]
  updatePreview()
}

async function download() {
  if (!hit.value || !cues.value.length || exportBlocked.value || downloading.value) return
  submittingExport.value = true
  clipError.value = ''
  const selectedMode = outputMode.value
  const quote = activeCues.value.map((c) => c.text).join(' ').trim()
  try {
    await startClipExport({
      hit: hit.value,
      title: titleLine.value || hit.value.showTitle || hit.value.mediaTitle || 'Clip',
      quote,
      start: requestStart.value,
      end: requestEnd.value,
      mode: selectedMode,
      padMs: requestPadMs.value,
      subtitles: subtitleStyle.value,
    })
  } catch (err) {
    clipError.value = (err as Error).message
  } finally {
    submittingExport.value = false
  }
}

async function copyLink() {
  if (!hit.value || !cues.value.length || exportBlocked.value) return
  const url =
    window.location.origin +
    clipStreamURL(
      hit.value,
      requestStart.value,
      requestEnd.value,
      outputMode.value,
      false,
      requestPadMs.value,
      subtitleStyle.value,
    )
  try {
    await navigator.clipboard.writeText(url)
    window.dispatchEvent(
      new CustomEvent('app-toast', { detail: { text: 'Clip link copied', color: 'success' } }),
    )
  } catch {
    window.prompt('Copy this link', url)
  }
}

function formatGifRes(res: string): string {
  const [w, h] = res.split(':')
  if (h === '-2' && w) return `${w}px wide`
  if (w === '-2' && h) return `${h}p`
  if (w && h) return `${w}×${h}`
  return '480px wide'
}

function formatLimitSec(sec: number): string {
  return Number.isInteger(sec) ? `${sec}s` : `${sec.toFixed(1)}s`
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
.clip-load-error {
  margin: 20px;
}
.line.line-pending {
  grid-template-columns: 1fr;
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
.range-heading {
  display: flex;
  align-items: center;
  gap: 6px;
}
.range-extend {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--muted);
  background: transparent;
}
.range-extend:hover:not(:disabled) {
  color: var(--text);
  background: rgba(255, 255, 255, 0.05);
}
.range-extend:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.range-extend:disabled {
  opacity: 0.35;
  cursor: default;
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
.btn-secondary:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.05);
}
.btn-secondary:disabled {
  opacity: 0.5;
  cursor: default;
}
.hint {
  margin: 12px 0 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--faint);
}
.limit-msg {
  margin: 10px 0 0;
  padding: 8px 12px;
  border: 1px solid rgba(251, 191, 36, 0.35);
  background: rgba(251, 191, 36, 0.08);
  border-radius: 8px;
  font-size: 12.5px;
  color: #fde68a;
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
.progress-note {
  margin: 6px 0 0;
  font-size: 11px;
  color: var(--faint);
}
.side-col {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
  min-width: 0;
}
.transcript {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-height: 0;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.015);
  overflow: hidden;
}
.transcript.collapsed {
  flex: 0 0 auto;
}
.transcript-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 11px 14px;
  border-bottom: 1px solid var(--border);
}
.transcript.collapsed .transcript-head {
  border-bottom: none;
}
.transcript-toggle {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  background: transparent;
  border: none;
  padding: 0;
  color: inherit;
  cursor: pointer;
}
.transcript-toggle-left {
  display: flex;
  align-items: center;
  gap: 7px;
}
.chevron {
  color: var(--muted);
  transition: transform 140ms ease;
  flex-shrink: 0;
}
.chevron.rotated {
  transform: rotate(180deg);
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

<template>
  <div class="timing-panel">
    <div class="timing-head">
      <div class="timing-title-row">
        <span class="timing-heading">Start &amp; end</span>
        <span v-if="isCustom" class="custom-pill">Custom</span>
      </div>
      <button
        v-if="isCustom"
        type="button"
        class="link-btn"
        :disabled="disabled"
        @click="emit('reset')"
      >
        Reset to transcript
      </button>
    </div>

    <div class="timing-grid">
      <div class="bound-group">
        <div class="bound-head">
          <label class="bound-label" :for="startInputId">Start</label>
          <button
            type="button"
            class="playhead-btn"
            aria-label="Set start to current playback time"
            :title="playheadStartTitle"
            :disabled="!canSetStartToPlayhead"
            @click="setStartToPlayhead"
          >
            Set to playhead
          </button>
        </div>
        <div class="stepper-row">
          <button
            type="button"
            class="nudge-btn"
            aria-label="Start 1 second earlier"
            title="Start 1s earlier"
            :disabled="disabled || !canNudgeStartEarlier"
            @click="nudgeStart(-1)"
          >
            -1s
          </button>
          <button
            type="button"
            class="nudge-btn"
            aria-label="Start 0.5 seconds earlier"
            title="Start 0.5s earlier"
            :disabled="disabled || !canNudgeStartEarlier"
            @click="nudgeStart(-0.5)"
          >
            -.5s
          </button>
          <input
            :id="startInputId"
            v-model="startDraft"
            type="text"
            inputmode="decimal"
            autocomplete="off"
            spellcheck="false"
            class="time-input mono"
            aria-label="Clip start time"
            :disabled="disabled"
            @focus="startFocused = true"
            @blur="onStartBlur"
            @keydown="onStartKeydown"
          />
          <button
            type="button"
            class="nudge-btn"
            aria-label="Start 0.5 seconds later"
            title="Start 0.5s later"
            :disabled="disabled || !canNudgeStartLater"
            @click="nudgeStart(0.5)"
          >
            +.5s
          </button>
          <button
            type="button"
            class="nudge-btn"
            aria-label="Start 1 second later"
            title="Start 1s later"
            :disabled="disabled || !canNudgeStartLater"
            @click="nudgeStart(1)"
          >
            +1s
          </button>
        </div>
      </div>

      <div class="bound-group">
        <div class="bound-head">
          <label class="bound-label" :for="endInputId">End</label>
          <button
            type="button"
            class="playhead-btn"
            aria-label="Set end to current playback time"
            :title="playheadEndTitle"
            :disabled="!canSetEndToPlayhead"
            @click="setEndToPlayhead"
          >
            Set to playhead
          </button>
        </div>
        <div class="stepper-row">
          <button
            type="button"
            class="nudge-btn"
            aria-label="End 1 second earlier"
            title="End 1s earlier"
            :disabled="disabled || !canNudgeEndEarlier"
            @click="nudgeEnd(-1)"
          >
            -1s
          </button>
          <button
            type="button"
            class="nudge-btn"
            aria-label="End 0.5 seconds earlier"
            title="End 0.5s earlier"
            :disabled="disabled || !canNudgeEndEarlier"
            @click="nudgeEnd(-0.5)"
          >
            -.5s
          </button>
          <input
            :id="endInputId"
            v-model="endDraft"
            type="text"
            inputmode="decimal"
            autocomplete="off"
            spellcheck="false"
            class="time-input mono"
            aria-label="Clip end time"
            :disabled="disabled"
            @focus="endFocused = true"
            @blur="onEndBlur"
            @keydown="onEndKeydown"
          />
          <button
            type="button"
            class="nudge-btn"
            aria-label="End 0.5 seconds later"
            title="End 0.5s later"
            :disabled="disabled || !canNudgeEndLater"
            @click="nudgeEnd(0.5)"
          >
            +.5s
          </button>
          <button
            type="button"
            class="nudge-btn"
            aria-label="End 1 second later"
            title="End 1s later"
            :disabled="disabled || !canNudgeEndLater"
            @click="nudgeEnd(1)"
          >
            +1s
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  formatTimestamp,
  MIN_CLIP_SEC,
  parseTimestamp,
  roundMs,
  setClipEnd,
  setClipStart,
} from '../../lib/clipBounds'

const props = defineProps<{
  start: number
  end: number
  durationSec: number
  maxClipSec: number
  playheadSec: number | null
  isCustom: boolean
  disabled: boolean
}>()

const emit = defineEmits<{
  'adjust-start': [targetStart: number, immediate: boolean]
  'adjust-end': [targetEnd: number, immediate: boolean]
  reset: []
}>()

const startInputId = 'clip-start-input'
const endInputId = 'clip-end-input'

const startDraft = ref(formatTimestamp(props.start))
const endDraft = ref(formatTimestamp(props.end))
const startFocused = ref(false)
const endFocused = ref(false)

watch(
  () => props.start,
  (next) => {
    if (!startFocused.value) {
      startDraft.value = formatTimestamp(next)
    }
  },
)

watch(
  () => props.end,
  (next) => {
    if (!endFocused.value) {
      endDraft.value = formatTimestamp(next)
    }
  },
)

const limits = computed(() => ({
  durationSec: props.durationSec,
  maxClipSec: props.maxClipSec,
}))

const canNudgeStartEarlier = computed(
  () =>
    props.start > 0.001 &&
    (props.maxClipSec <= 0 || props.end - props.start < props.maxClipSec - 0.001),
)
const canNudgeStartLater = computed(() => props.end - props.start > MIN_CLIP_SEC + 0.001)
const canNudgeEndEarlier = computed(() => props.end - props.start > MIN_CLIP_SEC + 0.001)
const canNudgeEndLater = computed(
  () =>
    (props.durationSec <= 0 || props.end < props.durationSec - 0.001) &&
    (props.maxClipSec <= 0 || props.end - props.start < props.maxClipSec - 0.001),
)

const canSetStartToPlayhead = computed(
  () =>
    !props.disabled &&
    props.playheadSec !== null &&
    props.playheadSec < props.end - MIN_CLIP_SEC &&
    Math.abs(props.playheadSec - props.start) >= 0.05,
)

const canSetEndToPlayhead = computed(
  () =>
    !props.disabled &&
    props.playheadSec !== null &&
    props.playheadSec > props.start + MIN_CLIP_SEC &&
    Math.abs(props.playheadSec - props.end) >= 0.05,
)

const playheadStartTitle = computed(() =>
  props.playheadSec !== null
    ? `Set start to ${formatTimestamp(props.playheadSec)}`
    : 'Seek the preview to set start from the playhead',
)

const playheadEndTitle = computed(() =>
  props.playheadSec !== null
    ? `Set end to ${formatTimestamp(props.playheadSec)}`
    : 'Seek the preview to set end from the playhead',
)

function nudgeStart(deltaSec: number) {
  if (props.disabled) return
  const next = setClipStart({ start: props.start, end: props.end }, roundMs(props.start + deltaSec), limits.value)
  startDraft.value = formatTimestamp(next.start)
  emit('adjust-start', next.start, false)
}

function nudgeEnd(deltaSec: number) {
  if (props.disabled) return
  const next = setClipEnd({ start: props.start, end: props.end }, roundMs(props.end + deltaSec), limits.value)
  endDraft.value = formatTimestamp(next.end)
  emit('adjust-end', next.end, false)
}

function commitStart() {
  const parsed = parseTimestamp(startDraft.value)
  if (parsed === null) {
    startDraft.value = formatTimestamp(props.start)
    return
  }
  const next = setClipStart({ start: props.start, end: props.end }, parsed, limits.value)
  startDraft.value = formatTimestamp(next.start)
  if (Math.abs(next.start - props.start) >= 0.001) {
    emit('adjust-start', next.start, true)
  }
}

function commitEnd() {
  const parsed = parseTimestamp(endDraft.value)
  if (parsed === null) {
    endDraft.value = formatTimestamp(props.end)
    return
  }
  const next = setClipEnd({ start: props.start, end: props.end }, parsed, limits.value)
  endDraft.value = formatTimestamp(next.end)
  if (Math.abs(next.end - props.end) >= 0.001) {
    emit('adjust-end', next.end, true)
  }
}

function onStartBlur() {
  startFocused.value = false
  commitStart()
}

function onEndBlur() {
  endFocused.value = false
  commitEnd()
}

function onStartKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    e.preventDefault()
    commitStart()
    ;(e.target as HTMLInputElement).blur()
  } else if (e.key === 'Escape') {
    e.preventDefault()
    startDraft.value = formatTimestamp(props.start)
    ;(e.target as HTMLInputElement).blur()
  } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
    e.preventDefault()
    const step = e.shiftKey ? 1 : 0.1
    nudgeStart(e.key === 'ArrowUp' ? step : -step)
  }
}

function onEndKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    e.preventDefault()
    commitEnd()
    ;(e.target as HTMLInputElement).blur()
  } else if (e.key === 'Escape') {
    e.preventDefault()
    endDraft.value = formatTimestamp(props.end)
    ;(e.target as HTMLInputElement).blur()
  } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
    e.preventDefault()
    const step = e.shiftKey ? 1 : 0.1
    nudgeEnd(e.key === 'ArrowUp' ? step : -step)
  }
}

function setStartToPlayhead() {
  if (!canSetStartToPlayhead.value || props.playheadSec === null) return
  const next = setClipStart({ start: props.start, end: props.end }, props.playheadSec, limits.value)
  startDraft.value = formatTimestamp(next.start)
  emit('adjust-start', next.start, true)
}

function setEndToPlayhead() {
  if (!canSetEndToPlayhead.value || props.playheadSec === null) return
  const next = setClipEnd({ start: props.start, end: props.end }, props.playheadSec, limits.value)
  endDraft.value = formatTimestamp(next.end)
  emit('adjust-end', next.end, true)
}
</script>

<style scoped>
.timing-panel {
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.018);
}
.timing-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}
.timing-title-row {
  display: flex;
  align-items: center;
  gap: 7px;
}
.timing-heading {
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--faint);
}
.custom-pill {
  font-size: 10px;
  font-weight: 600;
  padding: 1px 6px;
  border-radius: 999px;
  border: 1px solid var(--accent-line);
  background: var(--accent-soft);
  color: var(--accent);
}
.link-btn {
  background: none;
  border: none;
  padding: 0;
  font-size: 11px;
  color: var(--muted);
  text-decoration: underline;
  text-underline-offset: 3px;
  cursor: pointer;
}
.link-btn:hover:not(:disabled) {
  color: var(--text);
}
.link-btn:disabled {
  opacity: 0.4;
  cursor: default;
}
.timing-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
.bound-group {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}
.bound-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}
.bound-label {
  font-size: 11px;
  font-weight: 500;
  color: var(--muted);
}
.playhead-btn {
  border: none;
  background: transparent;
  padding: 0;
  font-size: 10.5px;
  color: var(--muted);
  cursor: pointer;
  white-space: nowrap;
}
.playhead-btn:hover:not(:disabled) {
  color: var(--accent);
  text-decoration: underline;
  text-underline-offset: 2px;
}
.playhead-btn:disabled {
  opacity: 0.35;
  cursor: default;
}
.stepper-row {
  display: flex;
  align-items: stretch;
  gap: 3px;
  min-width: 0;
}
.nudge-btn {
  flex: none;
  height: 28px;
  padding: 0 6px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: rgba(255, 255, 255, 0.025);
  color: var(--muted);
  font-family: var(--font-mono);
  font-size: 10.5px;
  cursor: pointer;
}
.nudge-btn:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--border-strong);
  background: rgba(255, 255, 255, 0.06);
}
.nudge-btn:focus-visible,
.time-input:focus-visible,
.playhead-btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
.nudge-btn:disabled {
  opacity: 0.35;
  cursor: default;
}
.time-input {
  flex: 1 1 0;
  min-width: 68px;
  height: 28px;
  padding: 0 6px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--surface-2);
  color: var(--text);
  font-size: 11.5px;
  text-align: center;
}
.time-input:disabled {
  opacity: 0.4;
  cursor: default;
}
@media (max-width: 600px) {
  .timing-grid {
    grid-template-columns: 1fr;
  }
}
</style>

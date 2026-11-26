<template>
  <div
    v-if="styleConfig.enabled && lines.length > 0"
    class="sub-overlay-frame"
    :style="frameStyle"
    aria-hidden="true"
  >
    <div class="sub-overlay-box" :style="computedStyles.container">
      <div v-for="(line, i) in lines" :key="i" class="sub-overlay-line">
        <span class="sub-overlay-text" :style="computedStyles.text">{{ line }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, type CSSProperties } from 'vue'
import type { CueDTO } from '../../api'
import {
  activeSubtitleLines,
  computeVideoContentBox,
  subtitleOverlayStyles,
  type SubtitleStyle,
} from '../../lib/subtitles'

const props = defineProps<{
  styleConfig: SubtitleStyle
  cues: CueDTO[]
  clipStart: number
  currentTime: number
  videoWidth: number
  videoHeight: number
  containerWidth: number
  containerHeight: number
}>()

const contentBox = computed(() =>
  computeVideoContentBox(props.containerWidth, props.containerHeight, props.videoWidth, props.videoHeight),
)

const frameStyle = computed<CSSProperties>(() => {
  const b = contentBox.value
  if (b.width <= 0 || b.height <= 0) {
    return { inset: '0px' }
  }
  return {
    left: `${b.left.toFixed(1)}px`,
    top: `${b.top.toFixed(1)}px`,
    width: `${b.width.toFixed(1)}px`,
    height: `${b.height.toFixed(1)}px`,
  }
})

const effectiveHeight = computed(() => (contentBox.value.height > 0 ? contentBox.value.height : 360))

const computedStyles = computed(() => subtitleOverlayStyles(props.styleConfig, effectiveHeight.value))

const lines = computed(() =>
  activeSubtitleLines(
    props.cues,
    props.clipStart + props.currentTime,
    props.styleConfig.uppercase,
    props.currentTime < 0.05,
  ),
)
</script>

<style scoped>
.sub-overlay-frame {
  position: absolute;
  pointer-events: none;
  overflow: hidden;
  z-index: 2;
}
.sub-overlay-box {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  box-sizing: border-box;
}
.sub-overlay-line {
  max-width: 100%;
  text-align: center;
  white-space: pre-wrap;
  overflow-wrap: break-word;
}
.sub-overlay-text {
  display: inline;
  box-decoration-break: clone;
  -webkit-box-decoration-break: clone;
}
</style>

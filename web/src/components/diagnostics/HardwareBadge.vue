<template>
  <div class="hw">
    <div class="hw-top">
      <span class="hw-pill" :class="tone">
        <span class="hw-dot"></span>
        {{ label }}
      </span>
      <span v-if="detail" class="hw-detail">{{ detail }}</span>
    </div>
    <p class="hw-pipeline mono">{{ pipeline }}</p>
    <p class="hw-sub">{{ subline }}</p>
    <div v-if="fallbacks > 0" class="hw-fallback">
      <v-icon icon="mdi-alert-outline" size="14" class="mr-1" />
      {{ fallbacks }} CPU fallback{{ fallbacks === 1 ? '' : 's' }} since start
      <span v-if="hw?.stats.lastFallbackNote" class="hw-note mono">
        · {{ truncate(hw.stats.lastFallbackNote, 90) }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { GPUSummary, HardwareStatus } from '../../api'

const props = defineProps<{
  hardware: HardwareStatus | null
  gpu?: GPUSummary | null
}>()

const hw = computed(() => props.hardware)
const fallbacks = computed(() => hw.value?.stats.fallbacks ?? 0)

const backendNames: Record<string, string> = {
  nvenc: 'NVENC',
  qsv: 'QUICKSYNC',
  vaapi: 'VAAPI',
  videotoolbox: 'VIDEOTOOLBOX',
}

const label = computed(() => {
  const h = hw.value
  if (!h) return 'CHECKING HARDWARE'
  if (h.available) return `HARDWARE · ${backendNames[h.backend] ?? h.backend.toUpperCase()}`
  if (h.requested !== 'auto' && h.requested !== 'none') return 'FALLBACK · CPU'
  return 'SOFTWARE · CPU'
})

const tone = computed(() => {
  const h = hw.value
  if (!h) return 'unknown'
  if (h.available) return 'hw'
  if (h.requested !== 'auto' && h.requested !== 'none') return 'bad'
  return 'soft'
})

const detail = computed(() => {
  const h = hw.value
  if (!h) return ''
  if (h.available) {
    const name = props.gpu?.available && props.gpu.name ? props.gpu.name : h.device
    return name || 'detected'
  }
  return h.requested === 'none' ? 'disabled via HWACCEL' : 'software encoding'
})

const pipeline = computed(() => {
  const h = hw.value
  if (!h) return 'detecting decode → encode pipeline…'
  if (!h.available) return 'software decode → libx264'
  return `${h.decoder || 'software'} → ${h.encoder}`
})

const subline = computed(() => {
  const h = hw.value
  if (!h) return 'Hardware detection runs once at startup.'
  const parts = [`requested: ${h.requested}`, `encoder: ${h.encoder}`]
  if (h.device) parts.push(`device: ${h.device}`)
  if (!h.available && h.reason) parts.push(h.reason)
  if (h.available && h.stats.hwEncodes + h.stats.cpuRenders === 0) parts.push('detected, not used yet')
  return parts.join(' · ')
})

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}…` : s
}
</script>

<style scoped>
.hw {
  min-width: 0;
}
.hw-top {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.hw-pill {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 4px 10px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  border: 1px solid transparent;
}
.hw-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.hw-pill.hw {
  color: var(--success);
  background: rgba(74, 222, 128, 0.1);
  border-color: rgba(74, 222, 128, 0.35);
}
.hw-pill.soft {
  color: var(--muted);
  background: rgba(255, 255, 255, 0.04);
  border-color: var(--border-strong);
}
.hw-pill.bad {
  color: #fbbf24;
  background: rgba(251, 191, 36, 0.1);
  border-color: rgba(251, 191, 36, 0.36);
}
.hw-pill.unknown {
  color: var(--faint);
  border-color: var(--border);
}
.hw-detail {
  font-size: 13px;
  font-weight: 550;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hw-pipeline {
  margin: 10px 0 2px;
  font-size: 12px;
  color: var(--text);
}
.hw-sub {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--faint);
}
.hw-fallback {
  margin-top: 8px;
  padding: 6px 9px;
  border-radius: 7px;
  border: 1px solid rgba(251, 191, 36, 0.3);
  background: rgba(251, 191, 36, 0.07);
  font-size: 11.5px;
  color: #fcd34d;
}
.hw-note {
  color: var(--faint);
}
</style>

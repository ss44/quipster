<script setup lang="ts">
import { computed } from 'vue'
import type { StatProgressModel } from '../../lib/progress'

// The bottom-pinned footer of a stat card: a unit line plus a track. The unit
// line states what is being counted so the bar's meaning never has to be
// guessed, and the fill carries the fraction — an indeterminate strip while
// the work's total is unknowable, a bare track while nothing is countable.
const props = defineProps<StatProgressModel>()

const fillStyle = computed(() => {
  if (props.indeterminate || props.empty) return {}
  return { width: `${props.pct}%` }
})

const readout = computed(() => props.readout ?? (props.empty ? '—' : `${props.pct}%`))
</script>

<template>
  <div class="stat-footer">
    <div class="stat-progress-meta">
      <span>{{ label }}</span>
      <span class="mono">{{ readout }}</span>
    </div>
    <div class="stat-progress">
      <div class="fill" :class="{ indeterminate }" :style="fillStyle"></div>
    </div>
  </div>
</template>

<style scoped>
.stat-footer {
  margin-top: auto;
  padding-top: 12px;
}
.stat-progress-meta {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 5px;
  font-size: 10.5px;
  color: var(--faint);
}
</style>

<template>
  <div class="gauge">
    <div class="gauge-head">
      <p class="gauge-label">{{ label }}</p>
      <span v-if="metric?.stale" class="gauge-stale">stale</span>
    </div>
    <p class="gauge-value">
      {{ display }}
      <span v-if="unitLabel" class="gauge-unit">{{ unitLabel }}</span>
    </p>
    <p class="gauge-sub">{{ subline }}</p>
    <div v-if="pct !== null" class="gauge-bar">
      <div class="gauge-fill" :class="tone" :style="{ width: `${pct}%` }"></div>
    </div>
    <svg v-if="samples.length > 1" class="gauge-spark" viewBox="0 0 100 22" preserveAspectRatio="none" aria-hidden="true">
      <polyline :points="points" fill="none" stroke="currentColor" stroke-width="1.4" vector-effect="non-scaling-stroke" />
    </svg>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Metric } from '../../api'
import { fmtBytes } from '../../lib/format'

const props = withDefaults(
  defineProps<{
    label: string
    metric: Metric | null | undefined
    samples?: number[]
    max?: number
  }>(),
  { samples: () => [], max: 0 }
)

const available = computed(() => props.metric?.available ?? false)

const display = computed(() => {
  const m = props.metric
  if (!m || !m.available) return 'n/a'
  if (m.unit === 'bytes') {
    if (m.total && m.total > 0) return `${fmtBytes(m.value)} / ${fmtBytes(m.total)}`
    return fmtBytes(m.value)
  }
  if (m.unit === 'percent') return `${m.value.toFixed(0)}%`
  return Number.isInteger(m.value) ? String(m.value) : m.value.toFixed(1)
})

const unitLabel = computed(() => {
  const m = props.metric
  if (!m || !m.available) return ''
  if (m.unit === 'percent') return ''
  return ''
})

const subline = computed(() => {
  const m = props.metric
  if (!m) return 'waiting for sample'
  if (!m.available) return m.reason || 'not available'
  if (props.label === 'RAM' || props.label === 'VRAM') {
    return `${pctOfTotal.value.toFixed(0)}% used`
  }
  return m.reason || ''
})

const pct = computed(() => {
  const m = props.metric
  if (!m || !m.available) return null
  if (m.unit === 'percent') return clamp(m.value)
  if (m.total && m.total > 0) return clamp((m.value / m.total) * 100)
  return null
})

const pctOfTotal = computed(() => pct.value ?? 0)

const tone = computed(() => {
  const p = pct.value ?? 0
  if (p >= 90) return 'danger'
  if (p >= 70) return 'warn'
  return 'ok'
})

const points = computed(() => {
  const values = props.samples
  if (values.length < 2) return ''
  const max = props.max > 0 ? props.max : Math.max(1, ...values)
  return values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * 100
      const y = 22 - clamp((v / max) * 22)
      return `${x.toFixed(2)},${y.toFixed(2)}`
    })
    .join(' ')
})

function clamp(v: number): number {
  return Math.max(0, Math.min(100, v))
}

</script>

<style scoped>
.gauge {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 13px 15px 12px;
  min-width: 0;
}
.gauge-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.gauge-label {
  margin: 0;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--faint);
}
.gauge-stale {
  font-family: var(--font-mono);
  font-size: 9.5px;
  color: #fbbf24;
  border: 1px solid rgba(251, 191, 36, 0.3);
  border-radius: 4px;
  padding: 0 4px;
}
.gauge-value {
  margin: 7px 0 0;
  font-size: 19px;
  font-weight: 650;
  letter-spacing: -0.02em;
  line-height: 1.15;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.gauge-unit {
  margin-left: 5px;
  font-size: 11px;
  font-weight: 500;
  color: var(--faint);
}
.gauge-sub {
  margin: 4px 0 0;
  font-size: 11px;
  line-height: 1.4;
  color: var(--muted);
  min-height: 15px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gauge-bar {
  height: 3px;
  margin-top: 9px;
  border-radius: 2px;
  background: rgba(255, 255, 255, 0.07);
  overflow: hidden;
}
.gauge-fill {
  height: 100%;
  border-radius: 2px;
  transition: width 0.3s ease;
}
.gauge-fill.ok {
  background: var(--success);
}
.gauge-fill.warn {
  background: var(--accent);
}
.gauge-fill.danger {
  background: var(--danger);
}
.gauge-spark {
  display: block;
  width: 100%;
  height: 22px;
  margin-top: 6px;
  color: rgba(255, 209, 102, 0.55);
}
</style>

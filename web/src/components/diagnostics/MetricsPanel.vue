<template>
  <section class="gauges">
    <MetricGauge label="CPU" :metric="gauge?.cpu" :samples="history.cpu" :max="100" :pending="pending" />
    <MetricGauge label="Process CPU" :metric="gauge?.processCpu" :samples="history.processCpu" :max="cpuMax" :pending="pending" />
    <MetricGauge label="RAM" :metric="gauge?.ram" :samples="history.ram" :max="100" :pending="pending" />
    <MetricGauge label="VRAM" :metric="gauge?.vram" :samples="history.vram" :max="100" :pending="pending" />
    <MetricGauge label="GPU" :metric="gauge?.gpu" :samples="history.gpu" :max="100" :pending="pending" />
    <MetricGauge label="Swap" :metric="gauge?.swap" :pending="pending" />
  </section>
</template>

<script setup lang="ts">
import type { GaugeSnapshot } from '../../api'
import type { MetricHistory } from '../../lib/diagnostics'
import MetricGauge from './MetricGauge.vue'

defineProps<{
  gauge: GaugeSnapshot | null
  history: MetricHistory
  cpuMax: number
  pending?: boolean
}>()
</script>

<style scoped>
.gauges {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
@media (max-width: 1200px) {
  .gauges {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
@media (max-width: 640px) {
  .gauges {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>

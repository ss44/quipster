<template>
  <section class="panel hw-panel">
    <header class="panel-header">
      <span>Hardware acceleration</span>
      <span class="panel-hint">{{ driverHint }}</span>
    </header>
    <div class="panel-body hw-body">
      <HardwareBadge :hardware="hardware" :gpu="gpu" />
      <div class="hw-actions">
        <v-btn size="small" color="primary" variant="flat" :loading="exporting" @click="emit('export')">
          <v-icon icon="mdi-download" size="16" class="mr-1" /> Export bundle
        </v-btn>
        <v-btn size="small" variant="tonal" :loading="copying" @click="emit('copy-report')">
          <v-icon icon="mdi-content-copy" size="16" class="mr-1" /> Copy report
        </v-btn>
        <v-btn size="small" variant="text" @click="emit('open-logs')">
          <v-icon icon="mdi-text-box-outline" size="16" class="mr-1" /> Open logs
        </v-btn>
        <v-btn size="small" variant="text" :loading="probing" @click="emit('reprobe')">
          <v-icon icon="mdi-refresh" size="16" class="mr-1" /> Re-run probe
        </v-btn>
        <v-checkbox v-model="includePaths" density="compact" hide-details label="Include media paths in bundle" class="path-toggle" />
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { GPUSummary, HardwareStatus, SystemInfo } from '../../api'
import HardwareBadge from './HardwareBadge.vue'

const props = defineProps<{
  hardware: HardwareStatus | null
  gpu: GPUSummary | null
  system: SystemInfo | null
  exporting: boolean
  copying: boolean
  probing: boolean
}>()

const emit = defineEmits<{
  (event: 'export'): void
  (event: 'copy-report'): void
  (event: 'open-logs'): void
  (event: 'reprobe'): void
}>()

// v-model:include-paths keeps the export option owned by the view while the
// checkbox stays next to the action it modifies.
const includePaths = defineModel<boolean>('includePaths', { default: false })

const driverHint = computed(() =>
  props.system?.gpu?.driverVersion ? `driver ${props.system.gpu.driverVersion}` : 'active pipeline',
)
</script>

<style scoped>
.hw-panel {
  margin-bottom: 12px;
}
.hw-body {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  flex-wrap: wrap;
}
.hw-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.path-toggle {
  font-size: 11.5px;
  max-width: 220px;
}
</style>

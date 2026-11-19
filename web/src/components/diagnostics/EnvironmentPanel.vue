<template>
  <section class="panel env-panel">
    <header class="panel-header">
      <span>Environment</span>
      <span class="panel-hint">
        ffmpeg <HazeText v-if="pending" label="Loading ffmpeg version" text="version" />
        <template v-else>{{ system?.ffmpeg?.version || 'unavailable' }}</template>
      </span>
    </header>
    <div class="panel-body">
      <div class="env-grid">
        <div v-for="item in items" :key="item.label" class="env-item">
          <span class="env-label">{{ item.label }}</span>
          <HazeText v-if="pending" :label="`Loading ${item.label}`" text="Environment details" />
          <span v-else :class="{ mono: item.mono }">{{ system ? item.value : 'unavailable' }}</span>
        </div>
      </div>

      <div class="codec-block">
        <p class="env-label">Codec capabilities</p>
        <div class="chips">
          <HazeText v-if="pending" label="Loading codec capabilities" text="Available codecs" />
          <span v-for="codec in codecs" :key="codec" class="mini-chip mono">{{ codec }}</span>
          <span v-if="!pending && !codecs.length" class="faint">{{ system ? 'No codec details reported.' : 'Codec details unavailable.' }}</span>
        </div>
      </div>

      <div class="codec-block">
        <p class="env-label">Effective settings</p>
        <div class="chips">
          <HazeText v-if="pending" label="Loading effective settings" text="Runtime settings" />
          <span v-else-if="!Object.keys(system?.config ?? {}).length" class="faint">{{ system ? 'No settings reported.' : 'Settings unavailable.' }}</span>
          <span v-for="(value, key) in system?.config ?? {}" :key="key" class="mini-chip mono">
            {{ key }}={{ value }}
          </span>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { GPUSummary, SystemInfo } from '../../api'
import { codecCapabilities, fmtUptime } from '../../lib/diagnostics'
import { fmtBytes } from '../../lib/format'
import HazeText from '../HazeText.vue'

const props = defineProps<{
  system: SystemInfo | null
  gpu: GPUSummary | null
  pending?: boolean
}>()

const codecs = computed(() => codecCapabilities(props.system))
const items = computed(() => {
  const { system, gpu } = props
  return [
    { label: 'OS', value: system?.osName || '—' },
    { label: 'Kernel', value: system?.kernel || '—', mono: true },
    { label: 'Runtime', value: `${system?.container ? 'Docker' : 'Host'} · ${system?.goos}/${system?.goarch}` },
    { label: 'CPU', value: `${system?.cpuModel || '—'} (${system?.cpuCores ?? '—'} cores)` },
    { label: 'Quipster', value: `${system?.appVersion || 'dev'} · ${system?.goVersion || '—'}` },
    { label: 'Uptime', value: fmtUptime(system?.appUptimeSec ?? 0) },
    { label: 'GPU', value: gpu?.available ? gpu.name : gpu?.reason || 'not reported' },
    { label: 'Driver', value: gpu?.driverVersion || '—', mono: true },
    { label: 'Search', value: system?.meiliUp ? 'online' : 'offline' },
    { label: 'Database', value: fmtBytes(system?.dbBytes ?? 0) },
    { label: 'ffprobe', value: system?.ffprobe?.version || 'unavailable', mono: true },
    { label: 'Hardware accel', value: (system?.hwaccels ?? []).join(', ') || 'none', mono: true },
  ]
})
</script>

<style scoped>
.env-panel {
  margin-bottom: 12px;
}
.env-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px 20px;
}
@media (max-width: 900px) {
  .env-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .env-grid {
    grid-template-columns: 1fr;
  }
}
.env-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
  color: var(--text);
  min-width: 0;
  overflow-wrap: anywhere;
}
.env-label {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--faint);
  margin: 0 0 4px;
}
.codec-block {
  margin-top: 16px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}
.mini-chip {
  display: inline-block;
  padding: 2px 7px;
  border: 1px solid var(--border);
  border-radius: 5px;
  font-size: 10.5px;
  color: var(--muted);
  background: rgba(255, 255, 255, 0.03);
}
</style>

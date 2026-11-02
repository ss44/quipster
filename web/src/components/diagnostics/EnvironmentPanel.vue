<template>
  <section class="panel env-panel">
    <header class="panel-header">
      <span>Environment</span>
      <span class="panel-hint">ffmpeg {{ system?.ffmpeg?.version || 'unknown' }}</span>
    </header>
    <div class="panel-body">
      <div class="env-grid">
        <div class="env-item"><span class="env-label">OS</span><span>{{ system?.osName || '—' }}</span></div>
        <div class="env-item"><span class="env-label">Kernel</span><span class="mono">{{ system?.kernel || '—' }}</span></div>
        <div class="env-item"><span class="env-label">Runtime</span><span>{{ system?.container ? 'Docker' : 'Host' }} · {{ system?.goos }}/{{ system?.goarch }}</span></div>
        <div class="env-item"><span class="env-label">CPU</span><span>{{ system?.cpuModel || '—' }} ({{ system?.cpuCores ?? '—' }} cores)</span></div>
        <div class="env-item"><span class="env-label">Quipster</span><span>{{ system?.appVersion || 'dev' }} · {{ system?.goVersion || '—' }}</span></div>
        <div class="env-item"><span class="env-label">Uptime</span><span>{{ fmtUptime(system?.appUptimeSec ?? 0) }}</span></div>
        <div class="env-item"><span class="env-label">GPU</span><span>{{ gpu?.available ? gpu.name : gpu?.reason || 'not reported' }}</span></div>
        <div class="env-item"><span class="env-label">Driver</span><span class="mono">{{ gpu?.driverVersion || '—' }}</span></div>
        <div class="env-item"><span class="env-label">Search</span><span>{{ system?.meiliUp ? 'online' : 'offline' }}</span></div>
        <div class="env-item"><span class="env-label">Database</span><span>{{ fmtBytes(system?.dbBytes ?? 0) }}</span></div>
        <div class="env-item"><span class="env-label">ffprobe</span><span class="mono">{{ system?.ffprobe?.version || 'unavailable' }}</span></div>
        <div class="env-item"><span class="env-label">Hardware accel</span><span class="mono">{{ (system?.hwaccels ?? []).join(', ') || 'none' }}</span></div>
      </div>

      <div class="codec-block">
        <p class="env-label">Codec capabilities</p>
        <div class="chips">
          <span v-for="codec in codecs" :key="codec" class="mini-chip mono">{{ codec }}</span>
          <span v-if="!codecs.length" class="faint">No codec details reported.</span>
        </div>
      </div>

      <div class="codec-block">
        <p class="env-label">Effective settings</p>
        <div class="chips">
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

const props = defineProps<{
  system: SystemInfo | null
  gpu: GPUSummary | null
}>()

const codecs = computed(() => codecCapabilities(props.system))
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

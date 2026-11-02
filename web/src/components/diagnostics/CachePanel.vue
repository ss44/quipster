<template>
  <section class="panel">
    <header class="panel-header">
      <span>Storage &amp; cache</span>
      <span class="panel-hint">{{ fmtBytes(gauge?.cacheBytes ?? 0) }} cached</span>
    </header>
    <div class="panel-body">
      <div v-for="d in disks" :key="d.name" class="disk-row">
        <div class="disk-top">
          <span class="disk-name">{{ d.name }}</span>
          <span class="mono">{{ d.available ? `${fmtBytes(d.freeBytes)} free` : d.reason || 'unavailable' }}</span>
        </div>
        <div class="disk-path mono truncate">{{ d.path || '—' }}</div>
        <div class="bar"><div class="fill" :style="{ width: `${diskUsedPct(d)}%` }"></div></div>
      </div>
      <div v-if="!disks.length" class="idle">Waiting for storage sample…</div>

      <div class="cache-row">
        <span class="cache-label">Cache breakdown</span>
        <div class="chips">
          <span v-for="(bytes, kind) in gauge?.cacheParts ?? {}" :key="kind" class="mini-chip mono">
            {{ kind }} {{ fmtBytes(bytes) }}
          </span>
        </div>
      </div>
      <div class="load-row mono">
        load {{ gauge?.load?.available ? `${gauge.load.one.toFixed(2)} ${gauge.load.five.toFixed(2)} ${gauge.load.fifteen.toFixed(2)}` : 'n/a' }}
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DiskMetric, GaugeSnapshot } from '../../api'
import { diskUsedPct } from '../../lib/diagnostics'
import { fmtBytes } from '../../lib/format'

const props = defineProps<{ gauge: GaugeSnapshot | null }>()

const disks = computed<DiskMetric[]>(() => Object.values(props.gauge?.disks ?? {}))
</script>

<style scoped>
.disk-row {
  padding: 7px 0 9px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}
.disk-row:first-child {
  padding-top: 0;
}
.disk-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 12px;
  color: var(--text);
}
.disk-name {
  text-transform: capitalize;
  font-weight: 550;
}
.disk-path {
  font-size: 10.5px;
  color: var(--faint);
  margin: 2px 0 6px;
}
.bar {
  height: 3px;
  border-radius: 2px;
  background: rgba(255, 255, 255, 0.07);
  overflow: hidden;
}
.bar .fill {
  height: 100%;
  background: var(--accent);
  border-radius: 2px;
}
.cache-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding-top: 12px;
}
.cache-label {
  font-size: 11px;
  color: var(--faint);
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
.load-row {
  margin-top: 10px;
  font-size: 11px;
  color: var(--faint);
}
.idle {
  margin: 8px 0;
  font-size: 12px;
  color: var(--faint);
}
</style>

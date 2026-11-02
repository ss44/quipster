<template>
  <section class="panel">
    <header class="panel-header">
      <span>Performance</span>
      <v-select
        v-model="selectedWindow"
        :items="windows"
        density="compact"
        variant="outlined"
        hide-details
        class="window-select"
        @update:model-value="emit('change')"
      />
    </header>
    <div class="panel-body">
      <div class="perf-top">
        <div>
          <p class="perf-value">{{ throughputLabel(bench) }}<span class="perf-unit">jobs/min</span></p>
          <p class="perf-sub">completed throughput</p>
        </div>
        <div>
          <p class="perf-value">{{ readThroughputLabel(bench) }}<span v-if="bench?.readAvailable" class="perf-unit">MB/s</span></p>
          <p class="perf-sub">{{ bench?.readAvailable ? 'derived source read' : bench?.readReason || 'source read' }}</p>
        </div>
      </div>

      <div class="table-wrap">
        <table class="bench-table">
          <thead>
            <tr>
              <th>Job</th>
              <th>Count</th>
              <th>p50</th>
              <th>p95</th>
              <th>Max</th>
              <th>Wait</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="job in bench?.jobs ?? []" :key="job.type">
              <td class="mono">{{ job.type }}</td>
              <td class="mono">{{ job.count }}</td>
              <td class="mono">{{ fmtMs(job.p50Ms) }}</td>
              <td class="mono">{{ fmtMs(job.p95Ms) }}</td>
              <td class="mono">{{ fmtMs(job.maxMs) }}</td>
              <td class="mono">{{ fmtMs(job.avgWaitMs) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="!bench?.jobs?.length" class="idle">No completed jobs in this window.</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import type { Benchmarks } from '../../api'
import { fmtMs, readThroughputLabel, throughputLabel } from '../../lib/diagnostics'

defineProps<{ bench: Benchmarks | null }>()

const emit = defineEmits<{ (event: 'change'): void }>()

// The window selector is owned by the view because changing it triggers a
// benchmark request; the panel only reports that the selection changed.
const selectedWindow = defineModel<string>('window', { required: true })

const windows = ['1h', '6h', '24h', '7d']
</script>

<style scoped>
.window-select {
  max-width: 92px;
  margin-top: -6px;
}
.perf-top {
  display: flex;
  gap: 32px;
  margin-bottom: 12px;
}
.perf-value {
  margin: 0;
  font-size: 22px;
  font-weight: 650;
  letter-spacing: -0.02em;
}
.perf-unit {
  margin-left: 5px;
  font-size: 11px;
  font-weight: 500;
  color: var(--faint);
}
.perf-sub {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--faint);
}
.table-wrap {
  overflow-x: auto;
}
.bench-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.bench-table th {
  text-align: left;
  padding: 7px 10px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--faint);
  border-bottom: 1px solid var(--border);
}
.bench-table td {
  padding: 7px 10px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.045);
}
.bench-table tr:last-child td {
  border-bottom: none;
}
.idle {
  margin: 8px 0;
  font-size: 12px;
  color: var(--faint);
}
</style>

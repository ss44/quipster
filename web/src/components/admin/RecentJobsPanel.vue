<script setup lang="ts">
import { computed, ref } from 'vue'
import { useNow } from '@vueuse/core'
import type { Job } from '../../api'
import { fileBase, jobClockTime, jobDuration, statusClass } from '../../lib/jobs'

// Filters operate on the snapshot's recent window; chips carry live counts so
// an empty result is expected before clicking rather than surprising.
const props = defineProps<{ jobs: Job[] }>()

const now = useNow({ interval: 1000 })
const nowMs = computed(() => now.value.getTime())

/** Statuses offered as filter chips, in workflow order. */
const STATUS_FILTERS = [
  { value: 'queued', label: 'Queued' },
  { value: 'running', label: 'Running' },
  { value: 'done', label: 'Done' },
  { value: 'error', label: 'Failed' },
] as const

interface FilterOption {
  value: string
  label: string
  dot?: string
  count: number
}

const statusFilter = ref('all')
const typeFilter = ref('all')

const countBy = (items: Job[], key: (job: Job) => string): Map<string, number> => {
  const counts = new Map<string, number>()
  for (const item of items) {
    const value = key(item)
    counts.set(value, (counts.get(value) ?? 0) + 1)
  }
  return counts
}

const statusOptions = computed<FilterOption[]>(() => {
  const counts = countBy(props.jobs, (job) => job.status)
  return [
    { value: 'all', label: 'All', count: props.jobs.length },
    ...STATUS_FILTERS.map((status) => ({
      value: status.value,
      label: status.label,
      dot: statusClass(status.value),
      count: counts.get(status.value) ?? 0,
    })),
  ]
})

const typeOptions = computed<FilterOption[]>(() => {
  const counts = countBy(props.jobs, (job) => job.type)
  const types = [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  return [
    { value: 'all', label: 'All', count: props.jobs.length },
    ...types.map(([type, count]) => ({ value: type, label: type, count })),
  ]
})

// A type chip disappears when its jobs roll out of the recent window; fall
// back to "all" rather than silently filtering everything away.
const activeTypeFilter = computed(() =>
  typeOptions.value.some((option) => option.value === typeFilter.value) ? typeFilter.value : 'all',
)

const filteredJobs = computed(() =>
  props.jobs.filter(
    (job) =>
      (statusFilter.value === 'all' || job.status === statusFilter.value) &&
      (activeTypeFilter.value === 'all' || job.type === activeTypeFilter.value),
  ),
)
</script>

<template>
  <section class="panel jobs">
    <header class="panel-header">
      <span>Recent jobs</span>
      <span class="panel-hint">{{ filteredJobs.length }} shown</span>
    </header>
    <div class="jobs-filters">
      <div class="filter-row" role="group" aria-label="Filter jobs by status">
        <span class="filter-label">Status</span>
        <button
          v-for="option in statusOptions"
          :key="option.value"
          type="button"
          class="filter-chip"
          :class="{ active: statusFilter === option.value }"
          :aria-pressed="statusFilter === option.value"
          @click="statusFilter = option.value"
        >
          <span v-if="option.dot" class="status-dot" :class="option.dot"></span>
          {{ option.label }}
          <span class="chip-count">{{ option.count }}</span>
        </button>
      </div>
      <div class="filter-row" role="group" aria-label="Filter jobs by type">
        <span class="filter-label">Type</span>
        <button
          v-for="option in typeOptions"
          :key="option.value"
          type="button"
          class="filter-chip mono"
          :class="{ active: activeTypeFilter === option.value }"
          :aria-pressed="activeTypeFilter === option.value"
          @click="typeFilter = option.value"
        >
          {{ option.label }}
          <span class="chip-count">{{ option.count }}</span>
        </button>
      </div>
    </div>
    <div class="table-wrap">
      <table class="jobs-table">
        <thead>
          <tr>
            <th>Type</th>
            <th>File</th>
            <th>Status</th>
            <th>Lines</th>
            <th>Took</th>
            <th>Created</th>
            <th>Error</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="job in filteredJobs" :key="job.id">
            <td class="mono">{{ job.type }}</td>
            <td>
              <span class="file-title truncate">{{ job.mediaTitle || fileBase(job.mediaPath) || '—' }}</span>
              <span class="file-path mono truncate">{{ fileBase(job.mediaPath) }}</span>
            </td>
            <td>
              <span class="state"><span class="status-dot" :class="statusClass(job.status)"></span>{{ job.status }}</span>
            </td>
            <td class="mono">{{ job.mediaCues ? job.mediaCues : '—' }}</td>
            <td class="mono">{{ jobDuration(job, nowMs) }}</td>
            <td class="mono">{{ jobClockTime(job.createdAt) }}</td>
            <td>
              <span v-if="job.error" class="error-text truncate" :title="job.error">{{ job.error }}</span>
              <span v-else class="faint">—</span>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-if="!jobs.length" class="idle">No jobs yet — run a scan to populate the queue.</p>
      <p v-else-if="!filteredJobs.length" class="idle">No jobs match the current filter.</p>
    </div>
  </section>
</template>

<style scoped>
.jobs {
  margin-top: 0;
}
.jobs-filters {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
}
.filter-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.filter-label {
  flex: none;
  width: 42px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--faint);
}
.filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 10px;
  font-size: 11.5px;
  color: var(--muted);
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 999px;
  cursor: pointer;
  transition: color 120ms ease, border-color 120ms ease, background 120ms ease;
}
.filter-chip:hover {
  color: var(--text);
  border-color: var(--border-strong);
}
.filter-chip.active {
  color: var(--text);
  border-color: var(--accent-line);
  background: var(--accent-soft);
}
.filter-chip .status-dot {
  flex: none;
}
.chip-count {
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--faint);
}
.filter-chip.active .chip-count {
  color: var(--accent);
}
.table-wrap {
  overflow-x: auto;
  max-width: 100%;
}
.jobs-table {
  width: 100%;
  min-width: max-content;
  border-collapse: collapse;
  font-size: 12px;
}
.jobs-table th {
  text-align: left;
  padding: 9px 14px;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--faint);
  border-bottom: 1px solid var(--border);
}
.jobs-table td {
  padding: 9px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.045);
  vertical-align: top;
}
.jobs-table tr:last-child td {
  border-bottom: none;
}
.file-title {
  display: block;
  max-width: 280px;
  color: var(--text);
  font-size: 12px;
}
.file-path {
  display: block;
  max-width: 280px;
  color: var(--faint);
  font-size: 10.5px;
}
.state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  color: var(--muted);
  text-transform: lowercase;
}
.error-text {
  display: inline-block;
  max-width: 230px;
  color: #fca5a5;
}
.idle {
  margin: 8px 12px;
  font-size: 12px;
  color: var(--faint);
}
</style>

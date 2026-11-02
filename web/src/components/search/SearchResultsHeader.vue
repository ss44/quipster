<template>
  <header class="results-head">
    <div class="results-summary">
      <span class="results-count">{{ results?.totalHits ?? 0 }}</span>
      <span class="results-label">{{ (results?.totalHits ?? 0) === 1 ? 'match' : 'matches' }} for</span>
      <span class="results-query">“{{ query }}”</span>
      <span v-if="heroTitles" class="results-heroes">in {{ heroTitles }} {{ heroTitles === 1 ? 'title' : 'titles' }}</span>
    </div>
    <div class="results-tools">
      <div v-if="results?.semanticAvailable" class="mode-toggle" role="group" aria-label="Search mode">
        <button
          v-for="option in SEARCH_MODE_OPTIONS"
          :key="option.value"
          type="button"
          class="mode-btn"
          :class="{ active: activeMode === option.value }"
          :aria-pressed="activeMode === option.value"
          @click="emit('set-mode', option.value)"
        >
          {{ option.label }}
        </button>
      </div>
      <div class="results-hint">
        <span>{{ hint }}</span>
      </div>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { SearchMode, SearchResponse } from '../../api'
import { SEARCH_MODE_OPTIONS, heroCount, searchModeHint } from '../../lib/search'

const props = defineProps<{
  query: string
  results: SearchResponse | null
  activeMode: Exclude<SearchMode, ''>
}>()

const emit = defineEmits<{
  (event: 'set-mode', value: Exclude<SearchMode, ''>): void
}>()

const heroTitles = computed(() => heroCount(props.results?.hits))
const hint = computed(() => searchModeHint(props.activeMode))
</script>

<style scoped>
.results-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 20px;
}
.results-summary {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 13.5px;
}
.results-count {
  font-family: var(--font-mono);
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
}
.results-label,
.results-heroes {
  color: var(--muted);
}
.results-query {
  color: var(--text);
  font-weight: 500;
}
.results-tools {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}
.mode-toggle {
  display: inline-flex;
  padding: 3px;
  gap: 2px;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 9px;
}
.mode-btn {
  font-size: 12px;
  font-weight: 550;
  color: var(--muted);
  background: transparent;
  border: none;
  border-radius: 7px;
  padding: 5px 12px;
  cursor: pointer;
  transition: background 130ms ease, color 130ms ease;
}
.mode-btn:hover {
  color: var(--text);
}
.mode-btn.active {
  background: var(--surface-3);
  color: var(--text);
}
.results-hint {
  display: flex;
  gap: 6px;
  font-size: 12px;
  color: var(--faint);
}
</style>

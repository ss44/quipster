<template>
  <section v-if="matches.length" class="matches">
    <h2 class="section-title">Matches by title</h2>
    <ul class="match-list">
      <li v-for="match in matches" :key="match.mediaId" class="match-row">
        <span class="match-title">{{ match.label }}</span>
        <span class="match-count">{{ match.count }} {{ match.count === 1 ? 'line' : 'lines' }}</span>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import type { TitleMatchSummary } from '../../lib/search'

defineProps<{ matches: TitleMatchSummary[] }>()
</script>

<style scoped>
.matches {
  margin-top: 28px;
}
.section-title {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--faint);
  margin: 0 0 10px;
}
.match-list {
  list-style: none;
  margin: 0;
  padding: 0;
  border-top: 1px solid var(--border);
}
.match-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 9px 2px;
  border-bottom: 1px solid var(--border);
  font-size: 13px;
}
.match-title {
  color: var(--text);
}
.match-count {
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--faint);
}
</style>

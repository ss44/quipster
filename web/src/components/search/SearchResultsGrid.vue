<template>
  <div v-if="loading" class="grid">
    <div v-for="i in 6" :key="i" class="scene-card skeleton">
      <div class="skeleton-thumb"></div>
      <div class="skeleton-line w-70"></div>
      <div class="skeleton-line w-95"></div>
      <div class="skeleton-line w-45"></div>
    </div>
  </div>

  <div v-else-if="results?.hits.length" class="grid">
    <HitCard
      v-for="hit in results.hits"
      :key="hit.chunkId"
      :hit="hit"
      :active-mode="activeMode"
      @open-clip="emit('open-clip', $event)"
    />
  </div>

  <div v-else-if="results" class="empty">
    <BrandLogo :size="54" :wordmark="false" class="empty-mark" />
    <p class="empty-title">No dialogue matched.</p>
    <p class="empty-copy">Try fewer words, a different phrasing, or make sure the title has been scanned.</p>
  </div>
</template>

<script setup lang="ts">
import type { Hit, SearchMode, SearchResponse } from '../../api'
import BrandLogo from '../BrandLogo.vue'
import HitCard from './HitCard.vue'

defineProps<{
  results: SearchResponse | null
  loading: boolean
  activeMode: Exclude<SearchMode, ''>
}>()

const emit = defineEmits<{ (event: 'open-clip', hit: Hit): void }>()
</script>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 16px;
}
.skeleton {
  pointer-events: none;
  display: flex;
  flex-direction: column;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
}
.skeleton-thumb {
  aspect-ratio: 16/9;
  background: var(--surface-2);
}
.skeleton-line {
  height: 10px;
  margin: 14px 16px 0;
  border-radius: 4px;
  background: var(--surface-2);
}
.skeleton .skeleton-line:last-child {
  margin-bottom: 16px;
}
.w-45 {
  width: 45%;
}
.w-70 {
  width: 70%;
}
.w-95 {
  width: 95%;
}
.empty {
  padding: 56px 0;
  text-align: center;
}
.empty-mark {
  display: inline-flex;
  margin-bottom: 16px;
  opacity: 0.22;
}
.empty-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 6px;
}
.empty-copy {
  font-size: 13px;
  color: var(--muted);
  margin: 0;
}
</style>

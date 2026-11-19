<template>
  <div v-if="loading" class="grid">
    <div v-for="i in 6" :key="i" class="scene-card pending-scene">
      <HazeText class="pending-title" label="Loading result title" text="Film and episode" />
      <HazeText label="Loading matching dialogue" text="Finding the lines you remember from your library" />
      <HazeText class="pending-time" label="Loading scene time" text="00:00 - 00:00" />
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
import HazeText from '../HazeText.vue'

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
  grid-template-columns: repeat(auto-fill, minmax(min(300px, 100%), 1fr));
  gap: 16px;
}
.pending-scene {
  pointer-events: none;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 22px;
  min-height: 260px;
  padding: 24px;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
}
.pending-title {
  font-size: 18px;
}
.pending-time {
  font-family: var(--font-mono);
  font-size: 12px;
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

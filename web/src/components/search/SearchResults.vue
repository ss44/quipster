<template>
  <SearchResultsHeader
    :query="query"
    :results="results"
    :active-mode="activeMode"
    @set-mode="emit('set-mode', $event)"
  />

  <SearchResultsGrid
    :results="results"
    :loading="loading"
    :active-mode="activeMode"
    @open-clip="emit('open-clip', $event)"
  />

  <template v-if="!loading && results">
    <SearchMatches :matches="matches" />

    <div v-if="results.totalPages > 1" class="pager">
      <v-pagination
        v-model="page"
        :length="Math.min(results.totalPages, 50)"
        :total-visible="7"
        density="compact"
        active-color="primary"
        variant="text"
      />
    </div>
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Hit, SearchMode, SearchResponse } from '../../api'
import { summarizeMatches } from '../../lib/search'
import SearchMatches from './SearchMatches.vue'
import SearchResultsGrid from './SearchResultsGrid.vue'
import SearchResultsHeader from './SearchResultsHeader.vue'

const props = defineProps<{
  query: string
  results: SearchResponse | null
  loading: boolean
  activeMode: Exclude<SearchMode, ''>
}>()

const emit = defineEmits<{
  (event: 'set-mode', value: Exclude<SearchMode, ''>): void
  (event: 'open-clip', hit: Hit): void
}>()

// Page is owned by the view because it feeds the request; the results
// component only reflects and updates it.
const page = defineModel<number>('page', { required: true })

const matches = computed(() => summarizeMatches(props.results?.hits))
</script>

<style scoped>
.pager {
  display: flex;
  justify-content: center;
  margin-top: 26px;
}
</style>

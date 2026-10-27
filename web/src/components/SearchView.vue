<template>
  <v-container fluid class="pa-6">
    <v-row v-if="error">
      <v-col>
        <v-alert type="error" variant="tonal">{{ error }}</v-alert>
      </v-col>
    </v-row>

    <v-row v-if="!query && !loading">
      <v-col class="text-center py-16">
        <v-icon icon="mdi-movie-search" size="72" class="mb-4 text-primary" />
        <div class="text-h5 font-weight-light">Find the exact line. Clip it. Share it.</div>
        <div class="text-body-2 text-medium-emphasis mt-2">
          Search your library by subtitle dialogue, then trim and download the moment.
        </div>
      </v-col>
    </v-row>

    <v-row v-if="loading">
      <v-col v-for="n in 8" :key="n" cols="12" sm="6" md="4" lg="3">
        <v-skeleton-loader type="card, list-item-two-line" />
      </v-col>
    </v-row>

    <template v-if="results">
      <div v-if="results.hits.length" class="mb-2 text-body-2 text-medium-emphasis">
        {{ results.totalHits }} dialogue match{{ results.totalHits === 1 ? '' : 'es' }} for “{{ results.query }}”
      </div>
      <v-row>
        <v-col v-for="hit in results.hits" :key="hit.chunkId" cols="12" sm="6" md="4" lg="3">
          <v-card elevation="2" class="fill-height d-flex flex-column">
            <v-img :src="hit.thumbUrl" aspect-ratio="16/9" cover class="bg-grey-darken-3">
              <template #placeholder>
                <div class="d-flex align-center justify-center fill-height">
                  <v-progress-circular indeterminate size="24" />
                </div>
              </template>
            </v-img>
            <v-card-item>
              <v-card-title class="text-subtitle-1">{{ hit.mediaTitle }}</v-card-title>
              <v-card-subtitle>
                {{ hit.year || '' }}<template v-if="hit.showTitle && hit.season"> · S{{ pad(hit.season) }}E{{ pad(hit.episode) }}</template>
              </v-card-subtitle>
            </v-card-item>
            <v-card-text class="py-0 flex-grow-1">
              <div class="cue-context" v-if="hit.contextBefore?.length">
                <div v-for="c in hit.contextBefore" :key="'b' + c.idx" class="text-caption text-medium-emphasis truncate-2">{{ c.text }}</div>
              </div>
              <div class="text-body-2 my-1" v-html="hit.textMarked || hit.text" />
              <div class="cue-context" v-if="hit.contextAfter?.length">
                <div v-for="c in hit.contextAfter" :key="'a' + c.idx" class="text-caption text-medium-emphasis truncate-2">{{ c.text }}</div>
              </div>
              <div class="text-caption text-medium-emphasis mt-2">{{ fmt(hit.startSec) }} – {{ fmt(hit.endSec) }}</div>
            </v-card-text>
            <v-card-actions>
              <v-spacer />
              <v-btn color="primary" variant="tonal" prepend-icon="mdi-content-cut" @click="openClip(hit)">
                Clip
              </v-btn>
              <v-btn
                icon="mdi-download"
                variant="text"
                :href="clipStreamURL(hit, hit.startSec, hit.endSec, 'fast', true)"
                aria-label="Download chunk as-is"
              />
            </v-card-actions>
          </v-card>
        </v-col>
      </v-row>

      <v-row v-if="results.titleMatches.length">
        <v-col cols="12">
          <div class="text-subtitle-2 text-medium-emphasis mb-2">Title matches</div>
          <v-chip
            v-for="m in results.titleMatches"
            :key="m.mediaId"
            class="ma-1"
            prepend-icon="mdi-movie-open"
            variant="tonal"
          >
            {{ m.title }}<span v-if="m.noSubtitle" class="text-caption ml-1">(no subs)</span>
          </v-chip>
        </v-col>
      </v-row>

      <v-row v-if="!results.hits.length && !results.titleMatches.length && !loading">
        <v-col class="text-center text-medium-emphasis py-10">No dialogue matched. Try fewer words.</v-col>
      </v-row>

      <v-row v-if="results.totalPages > 1" justify="center">
        <v-pagination
          v-model="page"
          :length="Math.min(results.totalPages, 50)"
          :total-visible="7"
        />
      </v-row>
    </template>
  </v-container>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, clipStreamURL, type Hit, type SearchResponse } from '../api'

const route = useRoute()
const router = useRouter()
const app = ref<{ openClip: (h: Hit) => void } | null>(null)

const results = ref<SearchResponse | null>(null)
const loading = ref(false)
const error = ref('')
const page = ref(1)
const query = computed(() => String(route.query.q || ''))

watch(
  [query, page],
  async ([q, p]) => {
    if (!q) {
      results.value = null
      return
    }
    loading.value = true
    error.value = ''
    try {
      results.value = await api.search(q, String(route.query.show || ''), Number(p))
    } catch (e) {
      error.value = (e as Error).message || 'search failed'
      results.value = null
    } finally {
      loading.value = false
    }
  },
  { immediate: true }
)

function openClip(hit: Hit) {
  // ClipModal is mounted in App; use injected bridge
  window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
}

function fmt(s: number): string {
  const m = Math.floor(s / 60)
  const sec = Math.floor(s % 60)
  return `${m}:${String(sec).padStart(2, '0')}`
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<style scoped>
.truncate-2 {
  display: -webkit-box;
  -webkit-line-clamp: 1;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.cue-context :deep(*) {
  opacity: 0.7;
}
:deep(mark) {
  background: rgba(124, 77, 255, 0.35);
  color: inherit;
  padding: 0 1px;
  border-radius: 3px;
}
</style>
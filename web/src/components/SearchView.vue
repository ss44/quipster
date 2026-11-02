<template>
  <div class="page">
    <SearchLanding
      v-if="!query"
      :quotes="quotes"
      :examples="examples"
      @open-quote="openQuote"
      @run-query="runQuery"
    />

    <template v-else>
      <p v-if="error" class="error-banner">{{ error }}</p>

      <SearchResults
        v-model:page="page"
        :query="query"
        :results="results"
        :loading="loading"
        :active-mode="activeMode"
        @set-mode="setMode"
        @open-clip="openClip"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Hit, type Quote, type SearchMode, type SearchResponse } from '../api'
import { activeSearchMode, searchModeFromQuery } from '../lib/search'
import SearchLanding from './search/SearchLanding.vue'
import SearchResults from './search/SearchResults.vue'

const route = useRoute()
const router = useRouter()

const examples = ref<string[]>([])
const quotes = ref<Quote[]>([])

// mode mirrors the ?mode= query param so the chosen retrieval strategy is
// shareable; '' defers to the server (hybrid when an embedder is configured).
const mode = ref<SearchMode>(searchModeFromQuery(route.query.mode))
watch(
  () => route.query.mode,
  (value) => {
    mode.value = searchModeFromQuery(value)
  },
)

// activeMode falls back to what the server actually executed until the user
// picks a mode explicitly, so the toggle reflects the first response too.
const activeMode = computed(() => activeSearchMode(mode.value, results.value))

function setMode(value: Exclude<SearchMode, ''>) {
  if (mode.value === value) return
  page.value = 1
  void router.replace({ name: 'search', query: { q: query.value, mode: value } })
}

onMounted(() => {
  void api.quotes(4)
    .then(({ quotes: nextQuotes }) => {
      quotes.value = nextQuotes ?? []
    })
    .catch(() => {
      quotes.value = []
    })
  void api.examples()
    .then(({ examples: nextExamples }) => {
      examples.value = nextExamples
    })
    .catch(() => {
      examples.value = []
    })
})

function openQuote(quote: Quote) {
  window.dispatchEvent(new CustomEvent('open-clip', { detail: quote }))
}

const results = ref<SearchResponse | null>(null)
const loading = ref(false)
const error = ref('')
const page = ref(1)
const query = computed(() => (typeof route.query.q === 'string' ? route.query.q : ''))

async function load() {
  if (!query.value) {
    results.value = null
    return
  }
  loading.value = true
  error.value = ''
  try {
    results.value = await api.search(query.value, '', page.value, mode.value)
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

// Declared before the load watcher so a mode change resets the page within
// the same flush cycle and triggers only a single fetch.
watch([query, mode], () => (page.value = 1))
watch([query, page, mode], load, { immediate: true })

function runQuery(next: string) {
  const queryParams: Record<string, string> = { q: next }
  if (mode.value) queryParams.mode = mode.value
  router.push({ name: 'search', query: queryParams })
}

function openClip(hit: Hit) {
  window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
}
</script>

<style scoped>
.page {
  max-width: 1520px;
  margin: 0 auto;
  padding: 28px 24px 64px;
}
.error-banner {
  margin: 12px 0;
  padding: 10px 14px;
  border: 1px solid rgba(248, 113, 113, 0.35);
  background: rgba(248, 113, 113, 0.08);
  border-radius: 8px;
  font-size: 13px;
  color: #fca5a5;
}
</style>

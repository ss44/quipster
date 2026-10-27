<template>
  <v-container fluid class="pa-4 pa-md-6" max-width="1600">
    <v-alert v-if="error" type="error" variant="tonal" class="mb-4" density="compact">
      {{ error }}
    </v-alert>

    <!-- Hero / empty state -->
    <div v-if="!query && !loading" class="hero fade-in">
      <div class="hero-icon">
        <v-icon icon="mdi-movie-search-outline" size="34" />
      </div>
      <h1 class="hero-title">Find the exact line. Clip it. Share it.</h1>
      <p class="hero-sub">
        Search your library by subtitle dialogue, trim the moment, and download or share the clip.
      </p>
      <div class="example-row">
        <span class="text-caption text-medium-emphasis mr-2">Try:</span>
        <v-chip
          v-for="q in examples"
          :key="q"
          size="small"
          variant="tonal"
          class="example-chip"
          @click="runExample(q)"
        >
          {{ q }}
        </v-chip>
      </div>
      <div class="hint-row">
        <v-icon icon="mdi-lightbulb-on-outline" size="15" class="mr-1" />
        All words must match. Wrap a phrase in “quotes” for an exact match.
      </div>
    </div>

    <!-- Loading -->
    <v-row v-if="loading">
      <v-col v-for="n in 8" :key="n" cols="12" sm="6" md="4" lg="3">
        <v-skeleton-loader type="image, article" class="rounded-xl" />
      </v-col>
    </v-row>

    <template v-if="results && !loading">
      <div class="result-head">
        <div class="d-flex align-center flex-wrap gap-2">
          <v-icon icon="mdi-format-quote-close" size="18" class="text-primary" />
          <span class="text-body-2">
            <strong>{{ results.totalHits }}</strong>
            dialogue {{ results.totalHits === 1 ? 'match' : 'matches' }}
            <span class="text-medium-emphasis">for</span>
            <span class="mono">“{{ results.query }}”</span>
          </span>
          <v-chip v-if="uniqueMedia" size="x-small" variant="tonal">
            {{ uniqueMedia }} {{ uniqueMedia === 1 ? 'title' : 'titles' }}
          </v-chip>
        </div>
        <div class="text-caption text-medium-emphasis d-none d-md-block">
          All words must match · “quotes” for exact phrases
        </div>
      </div>

      <v-row v-if="results.hits.length">
        <v-col v-for="hit in results.hits" :key="hit.chunkId" cols="12" sm="6" md="4" lg="3" xl="2">
          <v-card class="result-card fill-height d-flex flex-column fade-in" elevation="0" @click="openClip(hit)">
            <div class="thumb-wrap">
              <v-img :src="hit.thumbUrl" aspect-ratio="16/9" cover class="bg-grey-darken-4">
                <template #placeholder>
                  <div class="d-flex align-center justify-center fill-height">
                    <v-progress-circular indeterminate size="22" />
                  </div>
                </template>
              </v-img>
              <span class="thumb-badge">{{ fmt(hit.startSec) }} – {{ fmt(hit.endSec) }}</span>
              <div class="thumb-play">
                <v-icon icon="mdi-play-circle" size="46" class="text-white" />
              </div>
            </div>

            <v-card-item class="pb-1">
              <v-card-title class="text-subtitle-2 font-weight-bold clamp-1">
                {{ hit.mediaTitle }}
              </v-card-title>
              <v-card-subtitle class="d-flex align-center gap-2">
                <span v-if="hit.year">{{ hit.year }}</span>
                <span v-if="hit.showTitle && hit.season" class="text-caption">
                  S{{ pad(hit.season) }}E{{ pad(hit.episode) }}
                </span>
              </v-card-subtitle>
            </v-card-item>

            <v-card-text class="py-1 flex-grow-1">
              <div v-if="hit.contextBefore?.length" class="context">
                <div v-for="c in hit.contextBefore" :key="'b' + c.idx" class="text-caption clamp-1">{{ c.text }}</div>
              </div>
              <div class="dialogue cue-line my-1" v-html="hit.textMarked || hit.text" />
              <div v-if="hit.contextAfter?.length" class="context">
                <div v-for="c in hit.contextAfter" :key="'a' + c.idx" class="text-caption clamp-1">{{ c.text }}</div>
              </div>
            </v-card-text>

            <v-card-actions class="hover-actions">
              <v-btn size="small" color="primary" variant="flat" prepend-icon="mdi-content-cut" @click.stop="openClip(hit)">
                Clip
              </v-btn>
              <v-spacer />
              <v-btn
                size="small"
                icon="mdi-download"
                variant="text"
                :href="clipStreamURL(hit, hit.startSec, hit.endSec, 'fast', true)"
                aria-label="Download chunk as-is"
                @click.stop
              />
            </v-card-actions>
          </v-card>
        </v-col>
      </v-row>

      <v-row v-if="results.titleMatches.length">
        <v-col cols="12">
          <div class="text-subtitle-2 text-medium-emphasis mb-2 mt-2">
            Title matches <span class="text-caption">(no subtitle dialogue to clip)</span>
          </div>
          <v-chip
            v-for="m in results.titleMatches"
            :key="m.mediaId"
            class="ma-1"
            prepend-icon="mdi-movie-open-outline"
            variant="tonal"
          >
            {{ m.title }}
            <span v-if="m.year" class="ml-1 text-caption">({{ m.year }})</span>
            <span v-if="m.noSubtitle" class="ml-1 text-caption text-disabled">no subs</span>
          </v-chip>
        </v-col>
      </v-row>

      <div v-if="!results.hits.length && !results.titleMatches.length" class="empty">
        <v-icon icon="mdi-text-search" size="42" class="mb-3 text-medium-emphasis" />
        <div class="text-body-1">No dialogue matched “{{ results.query }}”.</div>
        <div class="text-body-2 text-medium-emphasis mt-1">
          Try fewer words, check spelling, or scan more of your library from Admin.
        </div>
      </div>

      <v-row v-if="results.totalPages > 1" justify="center" class="mt-4">
        <v-pagination v-model="page" :length="Math.min(results.totalPages, 50)" :total-visible="7" rounded />
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

const examples = ['show me the money', 'you talking to me', 'may the force be with you', 'here\'s looking at you kid']

const results = ref<SearchResponse | null>(null)
const loading = ref(false)
const error = ref('')
const page = ref(1)
const query = computed(() => String(route.query.q || ''))

const uniqueMedia = computed(() => {
  if (!results.value) return 0
  return new Set(results.value.hits.map((h) => h.mediaId)).size
})

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

watch(query, () => {
  page.value = 1
})

function runExample(q: string) {
  router.push({ name: 'search', query: { q } })
}

function openClip(hit: Hit) {
  window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
}

function fmt(s: number): string {
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = Math.floor(s % 60)
  return h > 0
    ? `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`
    : `${m}:${String(sec).padStart(2, '0')}`
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<style scoped>
.hero {
  max-width: 720px;
  margin: 8vh auto 0;
  text-align: center;
}
.hero-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 64px;
  height: 64px;
  border-radius: 18px;
  background: rgba(139, 92, 246, 0.12);
  border: 1px solid rgba(139, 92, 246, 0.3);
  color: #a78bfa;
  margin-bottom: 18px;
}
.hero-title {
  font-size: clamp(22px, 3.2vw, 32px);
  font-weight: 700;
  letter-spacing: -0.01em;
}
.hero-sub {
  color: rgba(255, 255, 255, 0.6);
  margin-top: 8px;
}
.example-row {
  margin-top: 26px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: center;
  align-items: center;
}
.example-chip {
  cursor: pointer;
}
.example-chip:hover {
  background: rgba(139, 92, 246, 0.22);
}
.hint-row {
  margin-top: 18px;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.42);
  display: flex;
  align-items: center;
  justify-content: center;
}
.result-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 2px 4px 14px;
  gap: 12px;
  flex-wrap: wrap;
}
.dialogue {
  font-size: 13.5px;
  line-height: 1.45;
}
.context {
  color: rgba(255, 255, 255, 0.42);
}
.empty {
  text-align: center;
  padding: 60px 16px;
}
</style>
<template>
  <div class="page">
    <!-- Landing -->
    <section v-if="!query" class="landing">
      <p class="eyebrow">Subtitle search</p>
      <h1 class="landing-title">Find any line you remember.</h1>
      <p class="landing-sub">
        Search your film and TV library by what was said. Open the moment, trim it by subtitle line,
        and download or share the clip.
      </p>

      <div class="examples" v-if="examples.length > 0">
        <button
          v-for="example in examples"
          :key="example"
          type="button"
          class="example-row"
          @click="runQuery(example)"
        >
          <svg viewBox="0 0 16 16" width="16" height="16" class="example-icon" aria-hidden="true">
            <path d="M6.2 3.2H3.2v4.6h3V6.2h.9v4.4H4.3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
            <path d="M12.8 3.2h-3v4.6h3V6.2h.9v4.4h-3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
          </svg>
          <span class="example-text">{{ example }}</span>
          <span class="example-go">Search ↵</span>
        </button>
      </div>

      <p class="landing-hint">
        Press <kbd class="kbd">/</kbd> to focus search. Wrap words in “quotes” for an exact phrase.
      </p>
    </section>

    <!-- Results -->
    <template v-else>
      <p v-if="error" class="error-banner">{{ error }}</p>

      <header class="results-head">
        <div class="results-summary">
          <span class="results-count">{{ results?.totalHits ?? 0 }}</span>
          <span class="results-label">{{ (results?.totalHits ?? 0) === 1 ? 'match' : 'matches' }} for</span>
          <span class="results-query">“{{ query }}”</span>
          <span v-if="heroCount" class="results-heroes">in {{ heroCount }} {{ heroCount === 1 ? 'title' : 'titles' }}</span>
        </div>
        <div class="results-hint">
          <span>All words must match</span>
          <span class="dot">·</span>
          <span>“quoted” for exact phrase</span>
        </div>
      </header>

      <div v-if="loading" class="grid">
        <div v-for="i in 6" :key="i" class="scene-card skeleton">
          <div class="skeleton-thumb"></div>
          <div class="skeleton-line w-70"></div>
          <div class="skeleton-line w-95"></div>
          <div class="skeleton-line w-45"></div>
        </div>
      </div>

      <template v-else-if="results">
        <div v-if="results.hits.length" class="grid">
          <article
            v-for="hit in results.hits"
            :key="hit.chunkId"
            class="scene-card"
            tabindex="0"
            role="button"
            :aria-label="`Open clip: ${hit.mediaTitle}, ${timecode(hit.startSec)}`"
            @click="openClip(hit)"
            @keydown.enter.prevent="openClip(hit)"
          >
            <div class="thumb">
              <img :src="hit.thumbUrl" :alt="''" loading="lazy" />
              <span class="duration">{{ durationLabel(hit.startSec, hit.endSec) }}</span>
              <span class="play">
                <svg viewBox="0 0 32 32" width="34" height="34" aria-hidden="true">
                  <circle cx="16" cy="16" r="15" fill="rgba(10,11,13,.72)" stroke="rgba(255,255,255,.55)" />
                  <path d="M13 10.6v10.8L22 16z" fill="#fff" />
                </svg>
              </span>
            </div>

            <div class="scene-body">
              <div class="meta-row">
                <span class="show">{{ hit.showTitle || hit.mediaTitle }}</span>
                <span v-if="hit.season && hit.episode" class="episode">S{{ pad(hit.season) }}E{{ pad(hit.episode) }}</span>
                <span v-else-if="hit.year" class="episode">{{ hit.year }}</span>
              </div>

              <p class="quote" v-html="hit.textMarked"></p>

              <ul class="context" v-if="contextPreview(hit).length">
                <li v-for="line in contextPreview(hit)" :key="line.key">{{ line.text }}</li>
              </ul>

              <div class="foot">
                <span class="time">{{ timecode(hit.startSec) }} <span class="sep">–</span> {{ timecode(hit.endSec) }}</span>
                <div class="actions">
                  <button type="button" class="btn-text" @click.stop="openClip(hit)">Clip</button>
                  <a class="icon-btn" :href="clipUrl(hit, true)" title="Download this clip" @click.stop>
                    <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
                      <path d="M8 2.5v7m0 0 2.7-2.7M8 9.5 5.3 6.8M3 13h10" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                  </a>
                </div>
              </div>
            </div>
          </article>
        </div>

        <div v-else class="empty">
          <p class="empty-title">No dialogue matched.</p>
          <p class="empty-copy">Try fewer words, a different phrasing, or make sure the title has been scanned.</p>
        </div>

        <section v-if="matches.length" class="matches">
          <h2 class="section-title">Matches by title</h2>
          <ul class="match-list">
            <li v-for="m in matches" :key="m.mediaId" class="match-row">
              <span class="match-title">{{ m.label }}</span>
              <span class="match-count">{{ m.count }} {{ m.count === 1 ? 'line' : 'lines' }}</span>
            </li>
          </ul>
        </section>

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
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, clipStreamURL, type Hit, type SearchResponse } from '../api'

const route = useRoute()
const router = useRouter()

const examples = ref<string[]>([])

onMounted(async () => {
  try {
    examples.value = (await api.examples()).examples
  } catch {
    examples.value = []
  }
})

const results = ref<SearchResponse | null>(null)
const loading = ref(false)
const error = ref('')
const page = ref(1)
const query = computed(() => (typeof route.query.q === 'string' ? route.query.q : ''))

const matches = computed(() => {
  const map = new Map<number, { mediaId: number; label: string; count: number }>()
  for (const hit of results.value?.hits ?? []) {
    const entry = map.get(hit.mediaId)
    if (entry) entry.count++
    else map.set(hit.mediaId, { mediaId: hit.mediaId, label: hit.showTitle || hit.mediaTitle, count: 1 })
  }
  return [...map.values()].sort((a, b) => b.count - a.count).slice(0, 12)
})

const heroCount = computed(() => new Set((results.value?.hits ?? []).map((h) => h.mediaId)).size)

function contextPreview(hit: Hit): { key: string; text: string }[] {
  const out: { key: string; text: string }[] = []
  for (const c of hit.contextBefore ?? []) out.push({ key: `b${c.idx}`, text: c.text })
  for (const c of hit.contextAfter ?? []) out.push({ key: `a${c.idx}`, text: c.text })
  return out.slice(0, 2)
}

async function load() {
  if (!query.value) {
    results.value = null
    return
  }
  loading.value = true
  error.value = ''
  try {
    results.value = await api.search(query.value, '', page.value)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

watch([query, page], load, { immediate: true })
watch(query, () => (page.value = 1))

function runQuery(q: string) {
  router.push({ name: 'search', query: { q } })
}

function openClip(hit: Hit) {
  window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
}

function clipUrl(hit: Hit, download: boolean): string {
  return clipStreamURL(hit, hit.startSec, hit.endSec, 'fast', download)
}

function timecode(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  return h > 0
    ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
    : `${m}:${String(s).padStart(2, '0')}`
}

function durationLabel(start: number, end: number): string {
  const secs = Math.max(0, end - start)
  return secs >= 60 ? timecode(secs) : `${secs.toFixed(1).replace(/\.0$/, '')}s`
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<style scoped>
.page {
  max-width: 1520px;
  margin: 0 auto;
  padding: 28px 24px 64px;
}

/* landing */
.landing {
  max-width: 660px;
  margin: 9vh auto 0;
}
.landing-title {
  font-size: clamp(26px, 3vw, 36px);
  font-weight: 650;
  letter-spacing: -0.025em;
  line-height: 1.15;
  margin: 10px 0 10px;
}
.landing-sub {
  font-size: 14.5px;
  line-height: 1.6;
  color: var(--muted);
  max-width: 54ch;
}
.examples {
  display: grid;
  gap: 8px;
  margin-top: 28px;
  max-width: 560px;
}
.example-row {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 11px 14px;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 10px;
  color: var(--text);
  font-size: 13.5px;
  text-align: left;
  transition: border-color 130ms ease, background 130ms ease;
}
.example-row:hover {
  background: var(--surface-2);
  border-color: var(--border-strong);
}
.example-icon {
  width: 16px;
  height: 16px;
  color: var(--faint);
  flex: none;
  display: block;
}
.example-text {
  flex: 1;
}
.example-go {
  font-size: 11.5px;
  color: var(--faint);
  font-family: var(--font-mono);
}
.landing-hint {
  margin-top: 22px;
  font-size: 12px;
  color: var(--faint);
}

/* results head */
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
.results-hint {
  display: flex;
  gap: 6px;
  font-size: 12px;
  color: var(--faint);
}
.results-hint .dot {
  opacity: 0.6;
}

/* grid + cards */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 16px;
}
.scene-card {
  display: flex;
  flex-direction: column;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
  cursor: pointer;
  transition:border-color 140ms ease, transform 140ms ease, box-shadow 140ms ease;
  outline: none;
}
.scene-card:hover,
.scene-card:focus-visible {
  border-color: var(--border-strong);
  transform: translateY(-2px);
  box-shadow: var(--shadow-2);
}
.scene-card:focus-visible {
  box-shadow: 0 0 0 2px var(--accent-line);
}
.thumb {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #050608;
  overflow: hidden;
}
.thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.thumb::after {
  content: '';
  position: absolute;
  inset: auto 0 0 0;
  height: 40%;
  background: linear-gradient(to top, rgba(6, 7, 9, 0.62), transparent);
  pointer-events: none;
}
.duration {
  position: absolute;
  right: 8px;
  bottom: 8px;
  z-index: 1;
  font-family: var(--font-mono);
  font-size: 10.5px;
  padding: 2px 6px;
  border-radius: 5px;
  background: rgba(6, 7, 9, 0.78);
  color: #e9eaed;
}
.play {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  opacity: 0;
  transition: opacity 140ms ease;
  z-index: 2;
}
.scene-card:hover .play,
.scene-card:focus-visible .play {
  opacity: 1;
}

.scene-body {
  display: flex;
  flex-direction: column;
  gap: 7px;
  padding: 13px 14px 12px;
}
.meta-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.show {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.episode {
  flex: none;
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--faint);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 5px;
  line-height: 17px;
}
.episode-title {
  margin: 0;
  font-size: 12px;
  font-weight: 500;
  color: var(--muted);
}
.quote {
  margin: 2px 0 0;
  font-size: 14px;
  line-height: 1.55;
  color: #e6e8eb;
  display: -webkit-box;
  -webkit-line-clamp: 4;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.context {
  list-style: none;
  margin: 2px 0 0;
  padding: 0;
  display: grid;
  gap: 2px;
}
.context li {
  font-size: 12px;
  color: var(--faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 8px;
  padding-top: 10px;
  border-top: 1px solid rgba(255, 255, 255, 0.05);
}
.time {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--faint);
}
.time .sep {
  opacity: 0.5;
  padding: 0 2px;
}
.actions {
  display: flex;
  align-items: center;
  gap: 4px;
}
.btn-text {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--accent);
  background: transparent;
  border: none;
  padding: 6px 8px;
  border-radius: 7px;
}
.btn-text:hover {
  background: var(--accent-soft);
}
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 8px;
  color: var(--muted);
}
.icon-btn:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.06);
}

/* skeleton */
.skeleton {
  pointer-events: none;
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
.skeleton-card .skeleton-line:last-child {
  margin-bottom: 16px;
}
.w-45 { width: 45%; }
.w-70 { width: 70%; }
.w-95 { width: 95%; }

/* empty + matches */
.empty {
  padding: 56px 0;
  text-align: center;
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
.pager {
  display: flex;
  justify-content: center;
  margin-top: 26px;
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
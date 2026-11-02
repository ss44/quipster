<template>
  <section class="landing">
    <p class="eyebrow">Subtitle search</p>
    <h1 class="landing-title">Find any line you remember.</h1>
    <p class="landing-sub">
      Search your film and TV library by what was said. Open the moment, trim it by subtitle line,
      and download or share the clip.
    </p>

    <QuoteGrid v-if="quotes.length" :quotes="quotes" @open-quote="emit('open-quote', $event)" />

    <div v-if="!quotes.length && examples.length > 0" class="examples">
      <button
        v-for="example in examples"
        :key="example"
        type="button"
        class="example-row"
        @click="emit('run-query', example)"
      >
        <svg viewBox="0 0 16 16" width="16" height="16" class="example-icon" aria-hidden="true">
          <path d="M6.2 3.2H3.2v4.6h3V6.2h.9v4.4H4.3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
          <path d="M12.8 3.2h-3v4.6h3V6.2h.9v4.4h-3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
        </svg>
        <span class="example-text">{{ example }}</span>
        <span class="example-go">Search ↵</span>
      </button>
    </div>

    <p v-if="!quotes.length && !examples.length" class="landing-empty">
      No indexed dialogue yet. Choose your library folders in
      <RouterLink to="/admin">Library</RouterLink> and run a scan — real lines will appear here.
    </p>

    <p class="landing-hint">
      Press <kbd class="kbd">/</kbd> to focus search. Wrap words in “quotes” for an exact phrase.
    </p>
  </section>
</template>

<script setup lang="ts">
import { RouterLink } from 'vue-router'
import type { Quote } from '../../api'
import QuoteGrid from './QuoteGrid.vue'

defineProps<{
  quotes: Quote[]
  examples: string[]
}>()

const emit = defineEmits<{
  (event: 'open-quote', quote: Quote): void
  (event: 'run-query', query: string): void
}>()
</script>

<style scoped>
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
.landing-empty {
  margin-top: 22px;
  font-size: 13px;
  color: var(--muted);
  line-height: 1.6;
}
.landing-empty a {
  color: var(--accent);
  text-decoration: none;
}
.landing-empty a:hover {
  text-decoration: underline;
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
</style>

<template>
  <section class="quotes">
    <p class="quotes-label">From your library</p>
    <div class="quote-grid">
      <button
        v-for="quote in quotes"
        :key="quote.chunkId"
        type="button"
        class="quote-card"
        @click="emit('open-quote', quote)"
      >
        <span class="quote-text">“{{ quote.text }}”</span>
        <span class="quote-foot">
          <span class="quote-title">{{ quoteTitle(quote) }}</span>
          <span class="quote-time mono">{{ timecode(quote.startSec) }}</span>
        </span>
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import type { Quote } from '../../api'
import { quoteTitle, timecode } from '../../lib/search'

defineProps<{ quotes: Quote[] }>()

const emit = defineEmits<{ (event: 'open-quote', quote: Quote): void }>()
</script>

<style scoped>
.quotes {
  margin-top: 26px;
}
.quotes-label {
  margin: 0 0 10px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--faint);
}
.quote-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 10px;
}
.quote-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  text-align: left;
  padding: 14px 16px;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  cursor: pointer;
  transition: border-color 140ms ease, background 140ms ease;
}
.quote-card:hover {
  border-color: var(--accent-line);
  background: var(--surface-2);
}
.quote-text {
  font-size: 14px;
  line-height: 1.5;
  color: #e6e8eb;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.quote-foot {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  margin-top: auto;
}
.quote-title {
  font-size: 11.5px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.quote-time {
  font-size: 10.5px;
  color: var(--faint);
}
</style>

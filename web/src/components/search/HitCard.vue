<template>
  <article
    class="scene-card"
    tabindex="0"
    role="button"
    :aria-label="`Open clip: ${hit.mediaTitle}, ${timecode(hit.startSec)}`"
    @click="emit('open-clip', hit)"
    @keydown.enter.prevent="emit('open-clip', hit)"
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

      <ul v-if="context.length" class="context">
        <li v-for="line in context" :key="line.key">{{ line.text }}</li>
      </ul>

      <div class="foot">
        <span class="time">
          {{ timecode(hit.startSec) }} <span class="sep">–</span> {{ timecode(hit.endSec) }}
          <span v-if="activeMode !== 'keyword' && hit.score" class="score">{{ matchPct(hit.score) }}</span>
        </span>
        <div class="actions">
          <button type="button" class="btn-text" @click.stop="emit('open-clip', hit)">Clip</button>
          <a class="icon-btn" :href="downloadUrl" title="Download this clip" @click.stop>
            <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
              <path d="M8 2.5v7m0 0 2.7-2.7M8 9.5 5.3 6.8M3 13h10" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </a>
        </div>
      </div>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { clipStreamURL, type Hit, type SearchMode } from '../../api'
import { contextPreview, durationLabel, matchPct, pad, timecode } from '../../lib/search'

const props = defineProps<{
  hit: Hit
  activeMode: Exclude<SearchMode, ''>
}>()

const emit = defineEmits<{ (event: 'open-clip', hit: Hit): void }>()

const context = computed(() => contextPreview(props.hit))
const downloadUrl = computed(() =>
  clipStreamURL(props.hit, props.hit.startSec, props.hit.endSec, 'fast', true),
)
</script>

<style scoped>
.scene-card {
  display: flex;
  flex-direction: column;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
  cursor: pointer;
  transition: border-color 140ms ease, transform 140ms ease, box-shadow 140ms ease;
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
.score {
  margin-left: 8px;
  padding: 1px 6px;
  border: 1px solid var(--border);
  border-radius: 5px;
  font-size: 10px;
  color: var(--muted);
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
</style>

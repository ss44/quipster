<template>
  <div class="search-wrap">
    <v-menu
      v-model="menuOpen"
      :activator="fieldEl"
      :close-on-content-click="false"
      location="bottom"
      offset="6"
      :width="menuWidth"
      content-class="search-menu"
    >
      <template #default>
        <div class="menu-panel">
          <button
            v-for="(item, index) in suggestions"
            :key="item.text"
            type="button"
            class="menu-item"
            :class="{ hovered: index === active }"
            @mousedown.prevent="choose(item.text)"
            @mouseenter="active = index"
          >
            <span class="menu-text" v-html="item.textMarked || item.text" />
          </button>
          <p v-if="!suggestions.length" class="menu-empty">No matching lines</p>
          <div class="menu-foot">
            <span><kbd class="kbd">↵</kbd> search</span>
            <span><kbd class="kbd">esc</kbd> close</span>
          </div>
        </div>
      </template>
    </v-menu>

    <v-text-field
      ref="field"
      v-model="query"
      placeholder="Search dialogue across your library…"
      variant="solo"
      flat
      rounded="lg"
      density="compact"
      hide-details
      clearable
      class="search-field"
      @keydown.enter.prevent="submit"
      @keydown.down.prevent="move(1)"
      @keydown.up.prevent="move(-1)"
      @keydown.esc="close"
      @update:model-value="onInput"
      @focus="onFocus"
    >
      <template #prepend-inner>
        <svg class="search-icon" viewBox="0 0 16 16" width="15" height="15" aria-hidden="true">
          <circle cx="7" cy="7" r="4.6" fill="none" stroke="currentColor" stroke-width="1.4" />
          <path d="M10.4 10.4 L14 14" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
        </svg>
      </template>
      <template #append-inner>
        <kbd v-if="!query" class="key-hint">/</kbd>
      </template>
    </v-text-field>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Suggestion } from '../api'

const route = useRoute()
const router = useRouter()
const field = ref<{ $el?: HTMLElement } | null>(null)
const fieldEl = computed(() => field.value?.$el as HTMLElement | undefined)
const menuWidth = ref<number>()
const query = ref('')
const suggestions = ref<Suggestion[]>([])
const menuOpen = ref(false)
const active = ref(0)
let timer: number | undefined

watch(
  () => route.query.q,
  (value) => {
    const next = typeof value === 'string' ? value : ''
    if (next !== query.value) query.value = next
  },
  { immediate: true },
)

function onInput(value: string | null) {
  query.value = value ?? ''
  active.value = 0
  window.clearTimeout(timer)
  const text = query.value.trim()
  if (text.length < 2) {
    suggestions.value = []
    menuOpen.value = false
    return
  }
  timer = window.setTimeout(async () => {
    try {
      const res = await api.suggest(text)
      suggestions.value = res.suggestions ?? []
      menuOpen.value = suggestions.value.length > 0
    } catch {
      suggestions.value = []
      menuOpen.value = false
    }
  }, 150)
}

function onFocus() {
  if (fieldEl.value) menuWidth.value = fieldEl.value.getBoundingClientRect().width
  if (suggestions.value.length) menuOpen.value = true
}

function move(delta: number) {
  if (!menuOpen.value || !suggestions.value.length) return
  const count = suggestions.value.length
  active.value = (active.value + delta + count) % count
}

function choose(value: string) {
  void go(value)
}

function submit() {
  if (menuOpen.value && suggestions.value[active.value]) {
    void go(suggestions.value[active.value].text)
    return
  }
  const value = query.value.trim()
  if (value) void go(value)
}

async function go(value: string) {
  // Carry the current search mode along so switching queries does not reset it.
  const query: Record<string, string> = { q: value }
  if (typeof route.query.mode === 'string' && route.query.mode) query.mode = route.query.mode
  await router.push({ name: 'search', query })
  query.value = value
  menuOpen.value = false
  blur()
}

function close() {
  menuOpen.value = false
  blur()
}

function blur() {
  ;(field.value?.$el?.querySelector('input') as HTMLInputElement | undefined)?.blur()
}

function focus() {
  ;(field.value?.$el?.querySelector('input') as HTMLInputElement | undefined)?.focus()
}

function onKey(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  const typing = !!target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)
  if (event.key === '/' && !typing) {
    event.preventDefault()
    focus()
  }
}

onMounted(() => {
  document.addEventListener('keydown', onKey)
  window.addEventListener('quipster:focus-search', focus)
})

onUnmounted(() => {
  document.removeEventListener('keydown', onKey)
  window.removeEventListener('quipster:focus-search', focus)
  window.clearTimeout(timer)
})
</script>

<style scoped>
.search-wrap {
  width: 100%;
  max-width: 620px;
}
.search-field :deep(.v-field) {
  background: var(--surface-2) !important;
  border: 1px solid var(--border);
  border-radius: 10px !important;
  min-height: 40px;
  transition: border-color 140ms ease, box-shadow 140ms ease;
}
.search-field :deep(.v-field:hover) {
  border-color: var(--border-strong);
}
.search-field :deep(.v-field--focused) {
  border-color: var(--accent-line);
  box-shadow: 0 0 0 3px rgba(255, 209, 102, 0.08);
  background: var(--surface-3) !important;
}
.search-field :deep(input) {
  font-size: 13.5px;
  letter-spacing: 0.005em;
  color: var(--text);
}
.search-field :deep(input::placeholder) {
  color: var(--faint);
  opacity: 1;
}
.search-icon {
  color: var(--faint);
  margin-right: 2px;
}
.key-hint {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--faint);
  border: 1px solid var(--border-strong);
  border-radius: 4px;
  padding: 1px 5px;
  line-height: 16px;
}
.menu-panel {
  background: var(--surface-2);
  border: 1px solid var(--border-strong);
  border-radius: 10px;
  box-shadow: var(--shadow-3);
  overflow: hidden;
  padding: 4px;
}
.menu-item {
  display: block;
  width: 100%;
  text-align: left;
  padding: 8px 10px;
  border: none;
  background: transparent;
  border-radius: 7px;
  cursor: pointer;
}
.menu-item.hovered {
  background: rgba(255, 255, 255, 0.06);
}
.menu-text {
  font-size: 13px;
  line-height: 1.45;
  color: #d9dce1;
}
.menu-empty {
  margin: 0;
  padding: 12px 10px;
  font-size: 12.5px;
  color: var(--faint);
}
.menu-foot {
  display: flex;
  gap: 14px;
  padding: 7px 10px 4px;
  border-top: 1px solid var(--border);
  margin-top: 4px;
  font-size: 11px;
  color: var(--faint);
}
</style>
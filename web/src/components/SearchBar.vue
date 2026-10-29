<template>
  <div ref="wrap" class="search-wrap">
    <v-text-field
      ref="field"
      v-model="query"
      role="combobox"
      aria-haspopup="listbox"
      aria-autocomplete="list"
      aria-controls="search-suggestions"
      :aria-expanded="menuOpen ? 'true' : 'false'"
      :aria-activedescendant="activeDescendant"
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
      @keydown.esc="closeMenu"
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

    <Transition name="menu-fade">
      <div v-if="menuOpen" class="search-menu">
        <div id="search-suggestions" class="menu-panel" role="listbox" aria-label="Search suggestions">
          <button
            v-for="(row, index) in rows"
            :id="optionId(index)"
            :key="row.kind === 'raw' ? '__raw__' : row.text"
            type="button"
            role="option"
            class="menu-item"
            :class="{ 'active-row': index === active, 'raw-row': row.kind === 'raw' }"
            :aria-selected="index === active"
            @mousedown.prevent
            @click="activate(row)"
          >
            <span v-if="row.kind === 'raw'" class="menu-text">
              Search for <strong>“{{ row.text }}”</strong>
            </span>
            <span v-else class="menu-text" v-html="row.textMarked || row.text" />
          </button>
          <p v-if="!suggestions.length" class="menu-empty">No matching lines</p>
          <div class="menu-foot" aria-hidden="true">
            <span><kbd class="kbd">↵</kbd> search as typed</span>
            <span><kbd class="kbd">↑↓</kbd> browse, <kbd class="kbd">↵</kbd> open</span>
            <span><kbd class="kbd">esc</kbd> dismiss</span>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Suggestion } from '../api'

const route = useRoute()
const router = useRouter()
const field = ref<{ $el?: HTMLElement } | null>(null)
const wrap = ref<HTMLElement | null>(null)
const query = ref('')
const suggestions = ref<Suggestion[]>([])
const menuOpen = ref(false)
// Index into `rows`: the only writer is keyboard navigation, and every reset
// lands back on 0 — the raw query row. Hovering is pure CSS styling, so a
// passive menu-open state can never promote a suggestion into the submit path.
const active = ref(0)
let timer: number | undefined

type SuggestionRow = { kind: 'raw'; text: string } | { kind: 'suggestion'; text: string; textMarked: string }

// The menu always leads with a row for the literal query the user typed, so
// the raw freeform search stays one Enter/click away even while suggestions
// are listed below it.
const rows = computed<SuggestionRow[]>(() => {
  const raw = query.value.trim()
  const list: SuggestionRow[] = raw ? [{ kind: 'raw', text: raw }] : []
  for (const s of suggestions.value) list.push({ kind: 'suggestion', text: s.text, textMarked: s.textMarked })
  return list
})

function optionId(index: number): string {
  return `search-opt-${index}`
}

const activeDescendant = computed(() =>
  menuOpen.value && rows.value.length ? optionId(active.value) : undefined,
)

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
    // A late reply must never surface for a query the user already moved past
    // or a field they already left, so re-check before and after the fetch.
    const stale = () => text !== query.value.trim() || !fieldFocused()
    if (stale()) return
    try {
      const res = await api.suggest(text)
      if (stale()) return
      suggestions.value = res.suggestions ?? []
      menuOpen.value = suggestions.value.length > 0
      active.value = 0
    } catch {
      if (stale()) return
      suggestions.value = []
      menuOpen.value = false
    }
  }, 150)
}

function fieldFocused(): boolean {
  const input = field.value?.$el?.querySelector('input')
  return !!input && document.activeElement === input
}

function onFocus() {
  // The panel is anchored below the field by document layout instead of the
  // overlay positioner, so reopening on focus can no longer cover the input,
  // even when the on-screen keyboard shrinks the visual viewport. Re-focusing
  // also starts a fresh selection so Enter means "search as typed" until the
  // user navigates the list again.
  active.value = 0
  if (suggestions.value.length) menuOpen.value = true
}

function move(delta: number) {
  if (!rows.value.length) return
  // Arrow keys re-open a dismissed list and are the only way to move the
  // highlight onto a suggestion, making the following Enter an explicit pick.
  if (!menuOpen.value) {
    active.value = 0
    menuOpen.value = true
    return
  }
  active.value = (active.value + delta + rows.value.length) % rows.value.length
}

function activate(row: SuggestionRow) {
  if (row.kind === 'raw') {
    submitRaw()
    return
  }
  void go(row.text)
}

function submit() {
  // Enter activates the keyboard-highlighted row. The raw query row leads the
  // list at index 0 and the highlight resets there on every input, focus and
  // re-open, so a passive menu-open state always submits what was typed.
  const row = menuOpen.value ? rows.value[active.value] : undefined
  if (row) {
    activate(row)
    return
  }
  submitRaw()
}

function submitRaw() {
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

function closeMenu() {
  // Dismissing the list must not cost the user their text or caret, so the
  // menu alone is closed and the field keeps focus for continued typing.
  menuOpen.value = false
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

// Taps outside the field's container dismiss the panel, mirroring the old
// overlay's click-outside behavior. Taps inside it (notably on suggestion
// items) must not dismiss anything, so touch selection keeps the input
// focused instead of the panel stealing the touch.
function onPointerDown(event: Event) {
  const target = event.target as Node | null
  if (menuOpen.value && wrap.value && target && !wrap.value.contains(target)) {
    menuOpen.value = false
  }
}

onMounted(() => {
  document.addEventListener('keydown', onKey)
  document.addEventListener('pointerdown', onPointerDown)
  window.addEventListener('quipster:focus-search', focus)
})

onUnmounted(() => {
  document.removeEventListener('keydown', onKey)
  document.removeEventListener('pointerdown', onPointerDown)
  window.removeEventListener('quipster:focus-search', focus)
  window.clearTimeout(timer)
})
</script>

<style scoped>
.search-wrap {
  position: relative;
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
/* Z-index contract: sticky header 20 < search-menu 60 < dialogs/snackbars
   (Vuetify overlays, 2400+). The panel stays inside the header's stacking
   context, so it paints above page content yet can never stack above the
   input itself; being positioned by document layout, keyboard-driven visual
   viewport changes cannot displace it over the field. */
.search-menu {
  position: absolute;
  top: calc(100% + 8px);
  left: 0;
  right: 0;
  z-index: 60;
}
.menu-fade-enter-active,
.menu-fade-leave-active {
  transition: opacity 120ms ease, transform 120ms ease;
}
.menu-fade-enter-from,
.menu-fade-leave-to {
  opacity: 0;
  transform: translateY(-4px);
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
.menu-item.active-row,
.menu-item:hover {
  background: rgba(255, 255, 255, 0.06);
}
.menu-item.raw-row {
  border-bottom: 1px solid var(--border);
  border-radius: 7px 7px 2px 2px;
  margin-bottom: 3px;
}
.menu-text {
  font-size: 13px;
  line-height: 1.45;
  color: #d9dce1;
}
.menu-text strong {
  color: var(--text);
  font-weight: 600;
}
.menu-empty {
  margin: 0;
  padding: 12px 10px;
  font-size: 12.5px;
  color: var(--faint);
}
.menu-foot {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  row-gap: 2px;
  padding: 7px 10px 4px;
  border-top: 1px solid var(--border);
  margin-top: 4px;
  font-size: 11px;
  color: var(--faint);
}
</style>

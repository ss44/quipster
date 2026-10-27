<template>
  <div class="search-wrap">
    <v-autocomplete
      ref="field"
      v-model="selected"
      v-model:search="query"
      :items="suggestions"
      :loading="loading"
      :menu-props="{ maxHeight: 420 }"
      placeholder="Search any line of dialogue…"
      variant="solo-filled"
      flat
      density="comfortable"
      rounded="xl"
      hide-details
      clearable
      hide-no-data
      no-filter
      item-title="text"
      return-object
      class="search-field"
      prepend-inner-icon="mdi-magnify"
      @keyup.enter="submit(query)"
      @click:clear="onClear"
    >
      <template #item="{ props, item }">
        <v-list-item v-bind="props" :title="undefined" class="py-2">
          <div class="text-body-2 cue-line clamp-2" v-html="item.raw.textMarked || item.raw.text" />
        </v-list-item>
      </template>
      <template #append-inner>
        <span v-if="!query" class="hotkey-hint d-none d-md-inline">/</span>
      </template>
    </v-autocomplete>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useDebounceFn } from '@vueuse/core'
import { api, type Suggestion } from '../api'

const emit = defineEmits<{ (e: 'search', q: string): void }>()

const route = useRoute()
const field = ref<{ focus: () => void } | null>(null)
const selected = ref<Suggestion | null>(null)
const query = ref<string>('')
const suggestions = ref<Suggestion[]>([])
const loading = ref(false)

watch(
  () => route.query.q,
  (q) => {
    const s = typeof q === 'string' ? q : ''
    if (s && s !== query.value) query.value = s
  },
  { immediate: true }
)

const refresh = useDebounceFn(async () => {
  const text = (query.value || '').trim()
  if (text.length < 2) {
    suggestions.value = []
    return
  }
  loading.value = true
  try {
    const res = await api.suggest(text)
    suggestions.value = res.suggestions ?? []
  } catch {
    suggestions.value = []
  } finally {
    loading.value = false
  }
}, 180)

watch(query, () => refresh())

watch(selected, (s) => {
  if (s?.text) submit(s.text)
})

function submit(q: string | undefined) {
  const text = (q || '').trim()
  if (!text) return
  selected.value = null
  emit('search', text)
}

function onClear() {
  suggestions.value = []
}

function onKey(e: KeyboardEvent) {
  const el = e.target as HTMLElement | null
  const typing = !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)
  if (e.key === '/' && !typing) {
    e.preventDefault()
    nextTick(() => field.value?.focus())
  }
}

onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<style scoped>
.search-wrap {
  max-width: 880px;
  width: 100%;
  margin: 0 auto;
}
.search-field :deep(.v-field) {
  background: rgba(255, 255, 255, 0.055) !important;
  border: 1px solid rgba(255, 255, 255, 0.07);
  transition: border-color 150ms ease, box-shadow 150ms ease;
}
.search-field :deep(.v-field--focused) {
  border-color: rgba(139, 92, 246, 0.6);
  box-shadow: 0 0 0 4px rgba(139, 92, 246, 0.12);
}
.hotkey-hint {
  border: 1px solid rgba(255, 255, 255, 0.16);
  border-radius: 6px;
  padding: 0 6px;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.5);
  line-height: 20px;
}
</style>
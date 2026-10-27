<template>
  <v-autocomplete
    v-model="selected"
    v-model:search="query"
    :items="suggestions"
    :loading="loading"
    label="Search dialogue…"
    placeholder="Try “you talking to me?”"
    hide-details
    clearable
    auto-select-first
    item-title="text"
    return-object
    menu-icon=""
    prepend-inner-icon="mdi-magnify"
    class="search-bar"
    @keyup.enter="submit(query)"
    @click:clear="clear"
  >
    <template #item="{ props, item }">
      <v-list-item v-bind="props" :title="undefined">
        <span v-html="item.raw.textMarked || item.raw.text" />
      </v-list-item>
    </template>
    <template #no-data>
      <v-list-item>
        <span class="text-medium-emphasis">Type at least a few characters</span>
      </v-list-item>
    </template>
  </v-autocomplete>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { api, type Suggestion } from '../api'

const emit = defineEmits<{ (e: 'search', q: string): void }>()

const selected = ref<Suggestion | null>(null)
const query = ref<string>('')
const suggestions = ref<Suggestion[]>([])
const loading = ref(false)

const refresh = useDebounceFn(async () => {
  const q = (query.value || '').trim()
  if (q.length < 3) {
    suggestions.value = []
    return
  }
  loading.value = true
  try {
    const res = await api.suggest(q)
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

function clear() {
  suggestions.value = []
}
</script>
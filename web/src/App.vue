<template>
  <v-app>
    <v-app-bar class="glass app-bar" elevation="0" height="68">
      <v-container class="d-flex align-center gap-4 py-0" fluid>
        <RouterLink to="/" class="text-decoration-none d-flex align-center">
          <span class="brand-icon">
            <v-icon icon="mdi-filmstrip-box-multiple" size="20" />
          </span>
          <span class="brand text-h6 ml-2 d-none d-sm-inline">Quipster</span>
        </RouterLink>

        <SearchBar class="flex-grow-1 mx-2" @search="onSearch" />

        <v-tooltip text="Admin & indexing" location="bottom">
          <template #activator="{ props }">
            <v-btn
              v-bind="props"
              icon="mdi-tune-variant"
              variant="text"
              to="/admin"
              aria-label="Admin"
            />
          </template>
        </v-tooltip>
      </v-container>
    </v-app-bar>

    <v-main class="bg-transparent">
      <RouterView />
    </v-main>

    <ClipModal />

    <v-snackbar v-model="snack.show" :color="snack.color" timeout="2200" location="bottom">
      {{ snack.text }}
    </v-snackbar>
  </v-app>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, reactive } from 'vue'
import { useRouter } from 'vue-router'
import SearchBar from './components/SearchBar.vue'
import ClipModal from './components/ClipModal.vue'

const router = useRouter()
const snack = reactive({ show: false, text: '', color: 'success' })

function onSearch(q: string) {
  router.push({ name: 'search', query: { q } })
}

function onToast(e: Event) {
  const detail = (e as CustomEvent).detail as { text: string; color?: string }
  snack.text = detail.text
  snack.color = detail.color || 'success'
  snack.show = true
}

onMounted(() => window.addEventListener('app-toast', onToast as EventListener))
onUnmounted(() => window.removeEventListener('app-toast', onToast as EventListener))
</script>
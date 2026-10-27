<template>
  <v-app>
    <header class="app-header">
      <div class="header-inner">
        <RouterLink to="/" class="text-decoration-none d-flex align-center">
          <span class="wordmark">Quipster</span>
        </RouterLink>

        <div class="search-slot">
          <SearchBar @search="onSearch" />
        </div>

        <nav class="d-flex align-center" style="gap: 4px">
          <RouterLink to="/admin" class="nav-link d-none d-sm-inline-flex align-center">
            <v-icon icon="mdi-tune-variant" size="15" class="mr-1" />
            Library
          </RouterLink>
          <RouterLink to="/admin" class="nav-link d-inline-flex d-sm-none" aria-label="Library">
            <v-icon icon="mdi-tune-variant" size="18" />
          </RouterLink>
        </nav>
      </div>
    </header>

    <v-main>
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

<style scoped>
.app-header {
  position: sticky;
  top: 0;
  z-index: 20;
}
.header-inner {
  display: flex;
  align-items: center;
  gap: 24px;
  max-width: 1520px;
  margin: 0 auto;
  padding: 0 24px;
  height: 58px;
}
.search-slot {
  flex: 1;
  display: flex;
  justify-content: center;
  min-width: 0;
}
@media (max-width: 640px) {
  .header-inner {
    gap: 12px;
    padding: 0 14px;
  }
}
</style>
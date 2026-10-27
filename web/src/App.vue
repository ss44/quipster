<template>
  <v-app>
    <v-app-bar color="surface" elevation="1" flat>
      <v-container class="d-flex align-center gap-4" fluid>
        <RouterLink to="/" class="text-decoration-none">
          <span class="text-h6 font-weight-bold text-primary">Quipster</span>
        </RouterLink>
        <SearchBar class="flex-grow-1" @search="onSearch" />
        <v-btn icon="mdi-cog" variant="text" to="/admin" aria-label="Admin" />
      </v-container>
    </v-app-bar>
    <v-main class="bg-background">
      <RouterView />
    </v-main>
    <ClipModal />
  </v-app>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'
import SearchBar from './components/SearchBar.vue'
import ClipModal from './components/ClipModal.vue'

const router = useRouter()

function onSearch(q: string) {
  router.push({ name: 'search', query: { q } })
}
</script>
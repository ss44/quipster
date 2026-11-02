<template>
  <v-dialog v-model="open" max-width="1000" scrollable>
    <div class="log-panel">
      <header class="log-head">
        <div>
          <h2 class="log-title">Recent logs</h2>
          <p class="log-sub">{{ logs.length }} lines from the API process</p>
        </div>
        <div class="log-actions">
          <v-btn size="small" variant="text" :loading="loading" @click="emit('refresh')">Refresh</v-btn>
          <v-btn size="small" variant="tonal" @click="emit('copy')">Copy</v-btn>
          <v-btn size="small" variant="text" @click="open = false">Close</v-btn>
        </div>
      </header>
      <pre class="log-lines">{{ logs.join('\n') || 'No log lines captured yet.' }}</pre>
    </div>
  </v-dialog>
</template>

<script setup lang="ts">
defineProps<{
  logs: string[]
  loading: boolean
}>()

const emit = defineEmits<{
  (event: 'refresh'): void
  (event: 'copy'): void
}>()

const open = defineModel<boolean>('open', { required: true })
</script>

<style scoped>
.log-panel {
  display: flex;
  flex-direction: column;
  max-height: 80vh;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 14px;
  overflow: hidden;
}
.log-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.log-title {
  margin: 0;
  font-size: 16px;
  font-weight: 650;
}
.log-sub {
  margin: 2px 0 0;
  font-size: 11.5px;
  color: var(--faint);
}
.log-actions {
  display: flex;
  gap: 6px;
}
.log-lines {
  margin: 0;
  padding: 14px 16px;
  overflow: auto;
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  color: var(--muted);
  background: #0b0d10;
}
</style>

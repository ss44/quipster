<template>
  <v-dialog v-model="open" max-width="1000" scrollable>
    <div class="log-panel">
      <header class="log-head">
        <div>
          <h2 class="log-title">Recent logs</h2>
          <p class="log-sub">
            <HazeText v-if="pending" label="Loading log count" text="Recent log lines" />
            <template v-else>{{ logs ? `${logs.length} lines from the API process` : 'Log count unavailable' }}</template>
          </p>
        </div>
        <div class="log-actions">
          <v-btn size="small" variant="text" :disabled="loading" @click="emit('refresh')">Refresh</v-btn>
          <v-btn size="small" variant="tonal" :disabled="!logs?.length" @click="emit('copy')">Copy</v-btn>
          <v-btn size="small" variant="text" @click="open = false">Close</v-btn>
        </div>
      </header>
      <p v-if="error" role="alert" class="log-error">{{ error }}</p>
      <pre class="log-lines"><HazeText v-if="pending" label="Loading logs" text="Recent application log entries" /><template v-else>{{ logs ? logs.join('\n') || 'No log lines captured yet.' : 'Logs unavailable.' }}</template></pre>
    </div>
  </v-dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import HazeText from '../HazeText.vue'

const props = defineProps<{
  logs: string[] | null
  loading: boolean
  error?: string
}>()

const pending = computed(() => props.loading && props.logs === null)

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
.log-error {
  margin: 0;
  padding: 10px 16px;
  color: var(--danger);
}
</style>

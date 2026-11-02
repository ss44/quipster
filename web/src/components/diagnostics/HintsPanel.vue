<template>
  <section class="panel hints-panel">
    <header class="panel-header"><span>Troubleshooting hints</span></header>
    <div class="panel-body hints">
      <div v-for="(hint, index) in hints" :key="`${hint.tone}-${index}`" class="hint" :class="hint.tone">
        <v-icon :icon="hint.tone === 'bad' ? 'mdi-alert' : 'mdi-information-outline'" size="16" class="mr-2" />
        <span>{{ hint.text }}</span>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import type { DiagnosticHint } from '../../lib/diagnostics'

defineProps<{ hints: DiagnosticHint[] }>()
</script>

<style scoped>
.hints-panel {
  border-color: rgba(251, 191, 36, 0.3);
}
.hints {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.hint {
  display: flex;
  align-items: flex-start;
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--text);
}
.hint.bad {
  color: #fca5a5;
}
.hint.warn {
  color: #fcd34d;
}
</style>

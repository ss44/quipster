<script setup lang="ts">
import StatProgress from './StatProgress.vue'
import type { StatProgressModel } from '../../lib/progress'

// One card of the admin overview row: label, headline value, a free-form sub
// line, and an optional progress footer. The footer sticks to the card bottom
// so every bar in the row shares one baseline regardless of sub-line wrapping.
defineProps<{
  label: string
  /** Pre-formatted headline value, e.g. "1,024" or "2.1 GB". */
  value: string
  /** Small unit suffix rendered after the value ("files", "lines"). */
  unit?: string
  /** Present when the card has countable work to show; omitted otherwise. */
  progress?: StatProgressModel
}>()
</script>

<template>
  <div class="stat">
    <p class="stat-label">{{ label }}</p>
    <p class="stat-value">{{ value }}<span v-if="unit" class="stat-unit">{{ unit }}</span></p>
    <p class="stat-sub"><slot /></p>
    <StatProgress v-if="progress" v-bind="progress" />
  </div>
</template>

<style scoped>
.stat {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 14px 16px;
  /* Column flex lets the footer stick to the card bottom so every bar in
     the row sits on the same line regardless of sub-line wrapping. */
  display: flex;
  flex-direction: column;
}
.stat-label {
  margin: 0 0 8px;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--faint);
}
.stat-value {
  margin: 0;
  font-size: 24px;
  font-weight: 650;
  letter-spacing: -0.02em;
  line-height: 1.1;
}
.stat-unit {
  font-size: 12px;
  font-weight: 500;
  color: var(--faint);
  margin-left: 6px;
  letter-spacing: 0;
}
.stat-sub {
  margin: 6px 0 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--muted);
}
</style>

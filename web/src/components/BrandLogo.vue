<template>
  <span class="brand">
    <svg
      class="brand-mark"
      viewBox="0 0 64 64"
      :width="size"
      :height="size"
      aria-hidden="true"
    >
      <path
        fill-rule="evenodd"
        fill="currentColor"
        d="M21.5 15 H42.5 A8.5 8.5 0 0 1 51 23.5 V34.5 A8.5 8.5 0 0 1 42.5 43 H21.5 A8.5 8.5 0 0 1 13 34.5 V23.5 A8.5 8.5 0 0 1 21.5 15 Z M27 20.5 L45 29 L27 37.5 Z"
      />
      <path fill="currentColor" d="M24 42 L34 42 L14 55 Z" />
    </svg>
    <span v-if="wordmark" class="brand-name">Quipster</span>
  </span>
</template>

<script setup lang="ts">
withDefaults(
  defineProps<{
    size?: number
    wordmark?: boolean
  }>(),
  { size: 24, wordmark: true },
)
</script>

<style scoped>
.brand {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  color: var(--text);
}
.brand-mark {
  flex-shrink: 0;
  color: var(--accent);
  filter: drop-shadow(0 1px 2px rgba(0, 0, 0, 0.35));
}
.brand-name {
  font-weight: 700;
  font-size: 17px;
  letter-spacing: -0.01em;
  color: var(--text);
}
</style>

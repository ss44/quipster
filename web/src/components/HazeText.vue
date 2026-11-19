<template>
  <span class="haze-text" role="status" :aria-label="label">
    <span class="haze-text__ink" aria-hidden="true">{{ text }}</span>
  </span>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  label?: string
  text?: string
}>(), {
  label: 'Loading',
  text: 'Loading content',
})
</script>

<style scoped>
.haze-text {
  display: inline-block;
  max-width: 100%;
  vertical-align: baseline;
  color: var(--muted);
}
.haze-text__ink {
  display: block;
  overflow-wrap: anywhere;
  user-select: none;
  filter: blur(3px);
  opacity: 0.45;
  animation: text-haze 2.4s ease-in-out infinite alternate;
}
@keyframes text-haze {
  to {
    filter: blur(5px);
    opacity: 0.75;
  }
}
@media (prefers-reduced-motion: reduce) {
  .haze-text__ink {
    animation: none;
  }
}
</style>

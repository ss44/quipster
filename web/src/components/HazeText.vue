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
  color: transparent;
  background-image: linear-gradient(100deg, var(--muted) 20%, var(--text) 50%, var(--muted) 80%);
  background-size: 300% 100%;
  background-clip: text;
  opacity: 0.8;
  animation: text-haze 1.6s linear infinite;
}
@keyframes text-haze {
  from {
    background-position: 100% 0;
  }
  to {
    background-position: 0% 0;
  }
}
@media (prefers-reduced-motion: reduce), (forced-colors: active) {
  .haze-text__ink {
    animation: none;
    background-image: none;
    color: inherit;
    opacity: 0.6;
  }
}
</style>

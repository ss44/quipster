<template>
  <section class="panel optimization-panel">
    <header class="panel-header">
      <span>Media optimization checks</span>
      <span class="panel-hint">{{ optimization?.recommended ?? 0 }} of {{ optimization?.scanned ?? 0 }} probed files flagged</span>
    </header>
    <div class="panel-body">
      <p class="optimization-note">Metadata-only heuristics; Quipster never modifies source media. Counts refresh with this view after media is reprobed.</p>
      <div v-if="findings.length" class="optimization-list">
        <div v-for="finding in findings" :key="finding.code" class="optimization-item">
          <div class="optimization-top"><strong>{{ finding.title }}</strong><span class="mini-chip mono">{{ finding.count }} files</span></div>
          <p>{{ finding.detail }}</p>
          <code>{{ finding.guidance }}</code>
        </div>
      </div>
      <p v-else class="idle">{{ optimization?.scanned ? 'No optimization recommendations recorded.' : 'No probed media findings yet.' }}</p>
      <p class="optimization-note">GOP/keyframe spacing and MP4 moov placement require packet/atom inspection and are not inferred from these bounded probes. See the media diagnostics reference for targeted FFprobe commands.</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { MediaOptimizationStats } from '../../api'
import { optimizationFindings } from '../../lib/diagnostics'

const props = defineProps<{ optimization: MediaOptimizationStats | null }>()

const findings = computed(() => optimizationFindings(props.optimization))
</script>

<style scoped>
.optimization-panel {
  margin-bottom: 12px;
}
.optimization-note {
  margin: 0 0 10px;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--faint);
}
.optimization-list {
  display: grid;
  gap: 10px;
}
.optimization-item {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.02);
}
.optimization-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 12px;
}
.optimization-item p {
  margin: 6px 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--muted);
}
.optimization-item code {
  display: block;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--text);
}
.mini-chip {
  display: inline-block;
  padding: 2px 7px;
  border: 1px solid var(--border);
  border-radius: 5px;
  font-size: 10.5px;
  color: var(--muted);
  background: rgba(255, 255, 255, 0.03);
}
.idle {
  margin: 8px 0;
  font-size: 12px;
  color: var(--faint);
}
</style>

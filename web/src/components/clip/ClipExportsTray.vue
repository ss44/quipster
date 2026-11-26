<template>
  <div v-if="items.length > 0" ref="rootRef" class="exports-tray">
    <button
      type="button"
      class="nav-link exports-trigger d-inline-flex align-center"
      :class="{ active: open, 'has-attention': activeCount > 0 || uncollectedReadyCount > 0 }"
      :aria-expanded="open"
      aria-controls="clip-exports-popover"
      aria-label="Recent clip exports"
      @click="open = !open"
    >
      <span
        class="status-dot"
        :class="activeCount > 0 ? 'run' : uncollectedReadyCount > 0 ? 'ok' : ''"
        aria-hidden="true"
      />
      <span class="d-none d-sm-inline">Clips</span>
      <span class="exports-count mono">
        <template v-if="activeCount > 0">{{ activeCount }}</template>
        <template v-else-if="uncollectedReadyCount > 0">{{ uncollectedReadyCount }}</template>
        <template v-else>{{ items.length }}</template>
      </span>
    </button>

    <section
      v-if="open"
      id="clip-exports-popover"
      class="exports-popover"
      role="region"
      aria-label="Recent clip exports"
    >
      <header class="exports-head">
        <div>
          <span class="exports-title">Recent clips</span>
          <span v-if="activeCount > 0" class="exports-sub">
            {{ activeCount }} rendering in background
          </span>
        </div>
        <button
          v-if="hasCompleted"
          type="button"
          class="clear-btn"
          @click="clearCompletedClipExports"
        >
          Clear
        </button>
      </header>

      <div class="exports-list">
        <article v-for="item in items" :key="item.id" class="export-row">
          <div class="export-main">
            <div class="export-title-line">
              <span class="status-dot" :class="dotClass(item.status)" aria-hidden="true" />
              <span class="export-title truncate">{{ item.title }}</span>
              <span class="mode-pill">{{ modeLabel(item.mode) }}</span>
              <span v-if="item.hasSubtitles" class="mode-pill">SUBS</span>
            </div>

            <p v-if="item.quote" class="export-quote truncate">“{{ item.quote }}”</p>

            <div class="export-meta mono">
              <span>{{ fmtTime(item.start) }} – {{ fmtTime(item.end) }}</span>
              <span class="sep">·</span>
              <span>{{ statusText(item) }}</span>
            </div>

            <div v-if="item.status === 'queued' || item.status === 'running'" class="stat-progress export-bar">
              <div
                class="fill"
                :class="{ indeterminate: item.status === 'queued' || item.progress <= 0.02 }"
                :style="item.status === 'running' && item.progress > 0.02 ? { width: `${Math.round(item.progress * 100)}%` } : undefined"
              />
            </div>

            <p v-else-if="item.status === 'error'" class="export-err">
              {{ item.error || 'Render failed' }}
            </p>
          </div>

          <div class="export-actions">
            <button
              v-if="item.status === 'ready'"
              type="button"
              class="export-action primary"
              @click="triggerSavedClipDownload(item.id)"
            >
              Download
            </button>
            <button
              v-else-if="item.status === 'expired' || item.status === 'error'"
              type="button"
              class="export-action"
              :disabled="retryingId === item.id"
              @click="onRetry(item.id)"
            >
              {{ retryingId === item.id ? 'Queuing…' : 'Re-render' }}
            </button>
            <button
              type="button"
              class="dismiss-btn"
              :aria-label="`Remove ${item.title}`"
              title="Remove from list"
              @click="removeClipExport(item.id)"
            >
              ×
            </button>
          </div>
        </article>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import {
  useClipExports,
  type ClipExportItem,
  type ClipExportStatus,
} from '../../lib/clipDownloads'

const {
  items,
  activeCount,
  uncollectedReadyCount,
  hasCompleted,
  retryClipExport,
  triggerSavedClipDownload,
  removeClipExport,
  clearCompletedClipExports,
} = useClipExports()

const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const retryingId = ref<string | null>(null)

function onDocumentClick(e: MouseEvent) {
  if (!open.value || !rootRef.value) return
  if (!rootRef.value.contains(e.target as Node)) {
    open.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) {
    open.value = false
  }
}

onMounted(() => {
  document.addEventListener('click', onDocumentClick)
  document.addEventListener('keydown', onKeydown)
})

onUnmounted(() => {
  document.removeEventListener('click', onDocumentClick)
  document.removeEventListener('keydown', onKeydown)
})

async function onRetry(id: string) {
  retryingId.value = id
  try {
    await retryClipExport(id)
  } catch (err) {
    window.dispatchEvent(
      new CustomEvent('app-toast', {
        detail: { text: (err as Error).message || 'Re-render failed', color: 'error' },
      }),
    )
  } finally {
    retryingId.value = null
  }
}

function dotClass(status: ClipExportStatus): string {
  if (status === 'queued' || status === 'running') return 'run'
  if (status === 'ready') return 'ok'
  if (status === 'error') return 'err'
  return ''
}

function modeLabel(mode: ClipExportItem['mode']): string {
  if (mode === 'gif') return 'GIF'
  if (mode === 'fast') return 'FAST'
  return 'PRECISE'
}

function statusText(item: ClipExportItem): string {
  switch (item.status) {
    case 'queued':
      return 'Queued…'
    case 'running':
      return `Rendering ${Math.round(item.progress * 100)}%`
    case 'ready':
      return item.downloaded ? 'Ready' : 'Ready to download'
    case 'expired':
      return 'Expired from cache'
    case 'error':
      return 'Failed'
  }
}

function fmtTime(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  const pad = (v: number) => String(v).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}
</script>

<style scoped>
.exports-tray {
  position: relative;
}
.exports-trigger {
  gap: 6px;
  border: 1px solid transparent;
  background: transparent;
  cursor: pointer;
}
.exports-trigger.active,
.exports-trigger.has-attention {
  color: var(--text);
  border-color: var(--border-strong);
  background: rgba(255, 255, 255, 0.04);
}
.exports-count {
  font-size: 11px;
  padding: 0 5px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.08);
  color: var(--text);
}
.exports-popover {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  width: min(390px, calc(100vw - 24px));
  background: var(--surface-1);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-3);
  z-index: 50;
  overflow: hidden;
}
.exports-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--border);
}
.exports-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.exports-sub {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}
.clear-btn {
  border: none;
  background: transparent;
  color: var(--muted);
  font-size: 12px;
  cursor: pointer;
  padding: 2px 6px;
  border-radius: 6px;
}
.clear-btn:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.06);
}
.exports-list {
  max-height: 420px;
  overflow-y: auto;
}
.export-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
  padding: 11px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}
.export-row:last-child {
  border-bottom: none;
}
.export-main {
  min-width: 0;
  flex: 1;
}
.export-title-line {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.export-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text);
}
.mode-pill {
  flex: none;
  font-size: 9.5px;
  font-weight: 600;
  letter-spacing: 0.06em;
  padding: 1px 5px;
  border-radius: 4px;
  background: rgba(255, 255, 255, 0.07);
  color: var(--muted);
}
.export-quote {
  margin: 3px 0 0;
  font-size: 11.5px;
  color: var(--muted);
}
.export-meta {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 4px;
  font-size: 11px;
  color: var(--faint);
}
.export-bar {
  margin-top: 6px;
}
.export-err {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--danger);
}
.export-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  flex: none;
}
.export-action {
  padding: 4px 9px;
  border-radius: 6px;
  border: 1px solid var(--border-strong);
  background: rgba(255, 255, 255, 0.04);
  color: var(--text);
  font-size: 11.5px;
  font-weight: 500;
  cursor: pointer;
}
.export-action:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.09);
}
.export-action.primary {
  background: var(--accent-soft);
  border-color: var(--accent-line);
  color: var(--accent);
}
.export-action.primary:hover {
  background: rgba(255, 209, 102, 0.2);
}
.export-action:disabled {
  opacity: 0.5;
  cursor: default;
}
.dismiss-btn {
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--faint);
  font-size: 15px;
  line-height: 1;
  cursor: pointer;
}
.dismiss-btn:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.06);
}
</style>

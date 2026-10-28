<template>
  <div class="nodes-page">
    <section class="panel intro">
      <header class="panel-header"><span>Worker nodes</span></header>
      <div class="panel-body">
        <p class="lede">
          Worker nodes run the same quipster binary in <code class="mono">node</code> mode and pull
          probe and clip jobs from this server, adding processing capacity. Launch one with:
        </p>
        <pre class="launch mono">quipster node --server {{ origin }} --media /path/to/media</pre>
        <p class="hint faint">
          A node stays pending until you approve it below. Nodes mount the same media library
          locally; if a node mounts a subfolder instead of the whole media root, set
          <code class="mono">NODE_MEDIA_MAP=serverPrefix=localPath</code> on it.
        </p>
      </div>
    </section>

    <section v-if="pending.length" class="panel pending">
      <header class="panel-header">
        <span>Pending approval</span>
      </header>
      <div class="panel-body">
        <div v-for="n in pending" :key="n.node.id" class="pending-row">
          <span class="status-dot warn"></span>
          <span class="name">{{ n.node.name }}</span>
          <span class="faint mono">v{{ n.node.version || '?' }}</span>
          <span class="faint">asked to join {{ fmtTime(n.node.createdAt) }}</span>
          <div class="row-actions">
            <button type="button" class="btn-secondary" :disabled="busy === n.node.id" @click="approve(n.node)">
              Approve
            </button>
            <button type="button" class="btn-danger" :disabled="busy === n.node.id" @click="remove(n.node)">
              Deny
            </button>
          </div>
        </div>
      </div>
    </section>

    <section class="panel fleet">
      <header class="panel-header"><span>Cluster</span></header>
      <div class="table-wrap">
        <table class="nodes-table">
          <thead>
            <tr>
              <th>Node</th>
              <th>Status</th>
              <th>Current work</th>
              <th>Done</th>
              <th>Failed</th>
              <th>Last seen</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="v in fleet" :key="v.node.id">
              <td>
                <span class="name">{{ v.node.name }}</span>
                <span class="faint mono version">v{{ v.node.version || '?' }}</span>
              </td>
              <td>
                <span class="state">
                  <span class="status-dot" :class="dotClass(v)"></span>{{ statusLabel(v) }}
                </span>
              </td>
              <td>
                <div v-for="j in v.currentJobs" :key="j.id" class="work-row">
                  <span class="mono">{{ j.type }}</span>
                  <span class="truncate work-title">{{ j.mediaTitle || j.mediaPath || `#${j.id}` }}</span>
                  <span class="progress-track"><span class="progress-fill" :style="{ width: pct(j.progress) }"></span></span>
                  <span class="mono faint">{{ pct(j.progress) }}</span>
                </div>
                <span v-if="!v.currentJobs.length" class="faint">idle</span>
              </td>
              <td class="mono">{{ v.node.jobsDone }}</td>
              <td class="mono" :class="{ 'error-text': v.node.jobsFailed > 0 }">{{ v.node.jobsFailed }}</td>
              <td class="mono">{{ fmtTime(v.node.lastSeen) }}</td>
              <td>
                <div class="row-actions">
                  <button
                    v-if="v.node.status === 'approved'"
                    type="button"
                    class="btn-danger"
                    :disabled="busy === v.node.id"
                    @click="revoke(v.node)"
                  >
                    Revoke
                  </button>
                  <button type="button" class="btn-secondary" :disabled="busy === v.node.id" @click="remove(v.node)">
                    Remove
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!fleet.length && !pending.length" class="idle">
          No nodes yet — start one with <code class="mono">quipster node --server {{ origin }}</code> and approve it here.
        </p>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type NodeView } from '../api'

const views = ref<NodeView[]>([])
const busy = ref<number | null>(null)
const origin = window.location.origin
let timer: ReturnType<typeof setInterval> | null = null

const pending = computed(() => views.value.filter((v) => v.node.status === 'pending'))
const fleet = computed(() =>
  views.value
    .filter((v) => v.node.status !== 'pending')
    .sort((a, b) => Number(b.online) - Number(a.online) || a.node.name.localeCompare(b.node.name))
)

async function refresh() {
  try {
    views.value = (await api.nodes()).nodes
  } catch {
    /* the panel keeps the last known state on transient errors */
  }
}

async function act(node: NodeView['node'], fn: (id: number) => Promise<unknown>) {
  busy.value = node.id
  try {
    await fn(node.id)
    await refresh()
  } catch (e) {
    console.error('node action failed', e)
  } finally {
    busy.value = null
  }
}

const approve = (n: NodeView['node']) => act(n, api.approveNode)
const revoke = (n: NodeView['node']) => act(n, api.revokeNode)
const remove = (n: NodeView['node']) => act(n, api.deleteNode)

function dotClass(v: NodeView): string {
  if (v.node.status === 'revoked') return 'err'
  return v.online ? 'ok' : ''
}

function statusLabel(v: NodeView): string {
  if (v.node.status === 'revoked') return 'revoked'
  return v.online ? 'online' : 'offline'
}

function pct(p: number): string {
  return `${Math.round(Math.min(1, Math.max(0, p)) * 100)}%`
}

function fmtTime(ms: number): string {
  if (!ms) return '—'
  const diff = Date.now() - ms
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  return new Date(ms).toLocaleString()
}

onMounted(() => {
  refresh()
  timer = setInterval(refresh, 5000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.nodes-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-width: 1520px;
  margin: 0 auto;
  padding: 14px 24px 32px;
}
.panel {
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--panel);
  overflow: hidden;
}
.panel-header {
  display: flex;
  align-items: center;
  padding: 12px 16px;
  font-size: 13px;
  font-weight: 600;
  border-bottom: 1px solid var(--border);
}
.panel-body {
  padding: 14px 16px;
}
.lede {
  margin: 0 0 10px;
  font-size: 13.5px;
  line-height: 1.5;
}
.launch {
  margin: 0 0 10px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  font-size: 12.5px;
  overflow-x: auto;
}
.hint {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.55;
}
.pending-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 0;
  font-size: 13.5px;
}
.pending-row + .pending-row {
  border-top: 1px solid var(--border);
}
.name {
  font-weight: 600;
}
.row-actions {
  display: flex;
  gap: 8px;
  margin-left: auto;
}
.table-wrap {
  overflow-x: auto;
}
.nodes-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.nodes-table th {
  text-align: left;
  padding: 10px 16px;
  font-size: 11.5px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
}
.nodes-table td {
  padding: 10px 16px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.nodes-table tr:last-child td {
  border-bottom: none;
}
.version {
  margin-left: 6px;
  font-size: 11px;
}
.state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--muted);
  flex-shrink: 0;
}
.status-dot.ok {
  background: var(--ok, #4caf50);
}
.status-dot.err {
  background: var(--danger, #e0533d);
}
.status-dot.warn {
  background: var(--warn, #e0a53d);
}
.work-row {
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 380px;
}
.work-row + .work-row {
  margin-top: 4px;
}
.work-title {
  min-width: 0;
}
.progress-track {
  width: 90px;
  height: 4px;
  border-radius: 2px;
  background: var(--border);
  overflow: hidden;
  flex-shrink: 0;
}
.progress-fill {
  height: 100%;
  background: var(--accent);
  border-radius: 2px;
}
.btn-secondary,
.btn-danger {
  padding: 6px 12px;
  font-size: 12.5px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text);
  cursor: pointer;
}
.btn-secondary:hover:not(:disabled) {
  border-color: var(--accent);
}
.btn-secondary:disabled {
  opacity: 0.55;
  cursor: default;
}
.btn-danger {
  color: var(--danger, #e0533d);
  border-color: color-mix(in srgb, var(--danger, #e0533d) 45%, transparent);
}
.btn-danger:hover:not(:disabled) {
  border-color: var(--danger, #e0533d);
}
.btn-danger:disabled {
  opacity: 0.55;
  cursor: default;
}
.idle {
  padding: 14px 16px;
  margin: 0;
  color: var(--muted);
  font-size: 13px;
}
.error-text {
  color: var(--danger, #e0533d);
}
.truncate {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (max-width: 640px) {
  .nodes-page {
    padding: 10px 14px 24px;
  }
}
</style>

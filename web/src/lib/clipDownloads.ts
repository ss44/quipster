import { computed, ref } from 'vue'
import { api, clipQueryParams, type ClipExportMode, type Hit } from '../api'
import type { SubtitleStyle } from './subtitles'

export type ClipExportStatus = 'queued' | 'running' | 'ready' | 'error' | 'expired'

export interface ClipExportItem {
  id: string
  cacheKey: string
  jobId: number | null
  mediaId: number
  title: string
  quote: string
  mode: ClipExportMode
  start: number
  end: number
  padMs: number
  hasSubtitles: boolean
  query: string
  downloadUrl: string
  filename: string
  status: ClipExportStatus
  progress: number
  error: string
  createdAt: number
  completedAt: number | null
  downloaded: boolean
  autoDownload: boolean
}

export interface StartClipExportInput {
  hit: Pick<Hit, 'mediaId'>
  title: string
  quote: string
  start: number
  end: number
  mode: ClipExportMode
  padMs: number
  subtitles?: SubtitleStyle | null
}

export const CLIP_EXPORTS_STORAGE_KEY = 'quipster.clipExports.v1'
const MAX_STORED_EXPORTS = 20
const POLL_INTERVAL_MS = 1000

const items = ref<ClipExportItem[]>([])
let hydrated = false
let pollTimer: ReturnType<typeof setTimeout> | null = null
let pollingInFlight = false
let downloadHandler: (url: string, filename: string) => void = defaultDownloadTrigger

function defaultDownloadTrigger(url: string, filename: string) {
  if (typeof document === 'undefined') return
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  a.remove()
}

function emitToast(text: string, color: 'success' | 'info' | 'error' = 'success') {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent('app-toast', { detail: { text, color } }))
}

function safeStorage(): Storage | null {
  try {
    return typeof window !== 'undefined' && window.localStorage ? window.localStorage : null
  } catch {
    return null
  }
}

function normalizeStoredItem(raw: unknown): ClipExportItem | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  if (typeof r.cacheKey !== 'string' || !r.cacheKey || typeof r.downloadUrl !== 'string' || !r.downloadUrl) {
    return null
  }
  const mode = r.mode === 'fast' || r.mode === 'accurate' || r.mode === 'gif' ? r.mode : 'accurate'
  const status: ClipExportStatus =
    r.status === 'queued' || r.status === 'running' || r.status === 'ready' || r.status === 'error' || r.status === 'expired'
      ? r.status
      : 'ready'
  return {
    id: typeof r.id === 'string' && r.id ? r.id : r.cacheKey,
    cacheKey: r.cacheKey,
    jobId: typeof r.jobId === 'number' && Number.isFinite(r.jobId) ? r.jobId : null,
    mediaId: typeof r.mediaId === 'number' && Number.isFinite(r.mediaId) ? r.mediaId : 0,
    title: typeof r.title === 'string' ? r.title : 'Clip',
    quote: typeof r.quote === 'string' ? r.quote : '',
    mode,
    start: typeof r.start === 'number' && Number.isFinite(r.start) ? r.start : 0,
    end: typeof r.end === 'number' && Number.isFinite(r.end) ? r.end : 0,
    padMs: typeof r.padMs === 'number' && Number.isFinite(r.padMs) ? r.padMs : 250,
    hasSubtitles: Boolean(r.hasSubtitles),
    query: typeof r.query === 'string' ? r.query : '',
    downloadUrl: r.downloadUrl,
    filename: typeof r.filename === 'string' && r.filename ? r.filename : `clip.${mode === 'gif' ? 'gif' : 'mp4'}`,
    status,
    progress: typeof r.progress === 'number' && Number.isFinite(r.progress) ? Math.max(0, Math.min(1, r.progress)) : 0,
    error: typeof r.error === 'string' ? r.error : '',
    createdAt: typeof r.createdAt === 'number' && Number.isFinite(r.createdAt) ? r.createdAt : Date.now(),
    completedAt: typeof r.completedAt === 'number' && Number.isFinite(r.completedAt) ? r.completedAt : null,
    downloaded: Boolean(r.downloaded),
    // Never auto-trigger browser downloads for exports loaded from an earlier
    // tab session; returning users see them ready in the Recent Clips tray.
    autoDownload: false,
  }
}

function persist() {
  const storage = safeStorage()
  if (!storage) return
  try {
    const trimmed = items.value.slice(0, MAX_STORED_EXPORTS).map((item) => ({
      ...item,
      autoDownload: false,
    }))
    storage.setItem(CLIP_EXPORTS_STORAGE_KEY, JSON.stringify(trimmed))
  } catch {
    /* ignore quota or private-mode storage errors */
  }
}

function upsertItem(next: ClipExportItem) {
  const filtered = items.value.filter((item) => item.cacheKey !== next.cacheKey)
  items.value = [next, ...filtered].slice(0, MAX_STORED_EXPORTS)
  persist()
}

function isInFlight(item: ClipExportItem): boolean {
  return item.status === 'queued' || item.status === 'running'
}

function schedulePoll() {
  if (pollTimer !== null) return
  if (!items.value.some(isInFlight)) return
  pollTimer = setTimeout(() => {
    pollTimer = null
    void pollInFlight()
  }, POLL_INTERVAL_MS)
}

function stopPoll() {
  if (pollTimer !== null) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function reconcileInFlightItem(item: ClipExportItem): Promise<void> {
  if (item.jobId !== null) {
    const status = await api.clipJobStatus(item.jobId)
    if (!status) {
      await reconcileByCacheKey(item, true)
      return
    }
    if (status.filename) {
      item.filename = status.filename
    }
    if (status.status === 'queued' || status.status === 'running') {
      item.status = status.status
      item.progress = status.status === 'running' ? Math.max(status.progress || 0, 0.02) : 0
      item.error = ''
      return
    }
    if (status.status === 'error') {
      item.status = 'error'
      item.error = status.error || 'render failed'
      item.autoDownload = false
      emitToast(`Clip render failed: ${item.title}`, 'error')
      return
    }
    if (status.status === 'done') {
      if (status.cached) {
        markReady(item)
      } else {
        item.status = 'expired'
        item.progress = 1
        item.autoDownload = false
      }
      return
    }
  }
  await reconcileByCacheKey(item, true)
}

async function reconcileByCacheKey(item: ClipExportItem, wasInFlight: boolean): Promise<void> {
  if (!item.cacheKey) {
    if (wasInFlight) {
      item.status = 'expired'
      item.autoDownload = false
    }
    return
  }
  const cache = await api.clipCacheStatus(item.cacheKey)
  if (cache.cached) {
    if (wasInFlight) {
      markReady(item)
    }
  } else {
    item.status = 'expired'
    item.autoDownload = false
  }
}

function markReady(item: ClipExportItem) {
  item.status = 'ready'
  item.progress = 1
  item.error = ''
  item.completedAt = Date.now()
  if (item.autoDownload && !item.downloaded) {
    item.autoDownload = false
    item.downloaded = true
    downloadHandler(item.downloadUrl, item.filename)
    emitToast(`Clip ready — downloading ${item.filename}`, 'success')
  } else if (!item.downloaded) {
    emitToast(`Clip ready in Recent Clips: ${item.title}`, 'success')
  }
}

export async function pollInFlight(): Promise<void> {
  if (pollingInFlight) return
  const active = items.value.filter(isInFlight)
  if (!active.length) return
  pollingInFlight = true
  try {
    await Promise.all(
      active.map(async (item) => {
        try {
          await reconcileInFlightItem(item)
        } catch {
          /* keep item in flight and retry on the next tick */
        }
      }),
    )
    persist()
  } finally {
    pollingInFlight = false
    if (items.value.some(isInFlight)) {
      schedulePoll()
    }
  }
}

export async function refreshCachedExports(): Promise<void> {
  const ready = items.value.filter((item) => item.status === 'ready')
  if (!ready.length) return
  await Promise.all(
    ready.map(async (item) => {
      try {
        await reconcileByCacheKey(item, false)
      } catch {
        /* ignore transient offline errors on initial load */
      }
    }),
  )
  persist()
}

export function initClipExports(): void {
  if (hydrated) return
  hydrated = true
  const storage = safeStorage()
  if (storage) {
    try {
      const raw = storage.getItem(CLIP_EXPORTS_STORAGE_KEY)
      if (raw) {
        const parsed = JSON.parse(raw)
        if (Array.isArray(parsed)) {
          items.value = parsed
            .map(normalizeStoredItem)
            .filter((item): item is ClipExportItem => item !== null)
            .slice(0, MAX_STORED_EXPORTS)
        }
      }
    } catch {
      items.value = []
    }
  }
  if (items.value.some(isInFlight)) {
    void pollInFlight()
  }
  if (items.value.some((item) => item.status === 'ready')) {
    void refreshCachedExports()
  }
}

export async function startClipExport(input: StartClipExportInput): Promise<ClipExportItem> {
  initClipExports()
  const params = clipQueryParams(input.hit, input.start, input.end, input.mode, input.padMs, input.subtitles)
  const query = params.toString()
  const res = await api.createClipJob(query)
  const now = Date.now()
  const ready = res.status === 'ready' || res.cached
  const item: ClipExportItem = {
    id: res.cacheKey,
    cacheKey: res.cacheKey,
    jobId: res.jobId ?? null,
    mediaId: input.hit.mediaId,
    title: input.title || 'Clip',
    quote: input.quote.trim().slice(0, 140),
    mode: input.mode,
    start: input.start,
    end: input.end,
    padMs: input.padMs,
    hasSubtitles: Boolean(input.subtitles?.enabled),
    query,
    downloadUrl: res.downloadUrl,
    filename: res.filename,
    status: ready ? 'ready' : 'queued',
    progress: ready ? 1 : 0,
    error: '',
    createdAt: now,
    completedAt: ready ? now : null,
    downloaded: ready,
    autoDownload: !ready,
  }
  upsertItem(item)
  if (ready) {
    downloadHandler(item.downloadUrl, item.filename)
    emitToast(`Downloading ${item.filename}`, 'success')
  } else {
    emitToast('Rendering clip in background — safe to browse or close preview', 'info')
    schedulePoll()
  }
  return item
}

export async function retryClipExport(id: string): Promise<ClipExportItem | null> {
  const existing = items.value.find((item) => item.id === id)
  if (!existing || !existing.query) return null
  const res = await api.createClipJob(existing.query)
  const now = Date.now()
  const ready = res.status === 'ready' || res.cached
  const next: ClipExportItem = {
    ...existing,
    cacheKey: res.cacheKey,
    id: res.cacheKey,
    jobId: res.jobId ?? null,
    downloadUrl: res.downloadUrl,
    filename: res.filename,
    status: ready ? 'ready' : 'queued',
    progress: ready ? 1 : 0,
    error: '',
    createdAt: now,
    completedAt: ready ? now : null,
    downloaded: ready,
    autoDownload: !ready,
  }
  upsertItem(next)
  if (ready) {
    downloadHandler(next.downloadUrl, next.filename)
  } else {
    schedulePoll()
  }
  return next
}

export function triggerSavedClipDownload(id: string): boolean {
  const item = items.value.find((entry) => entry.id === id)
  if (!item || item.status !== 'ready') return false
  item.downloaded = true
  item.autoDownload = false
  persist()
  downloadHandler(item.downloadUrl, item.filename)
  return true
}

export function removeClipExport(id: string): void {
  items.value = items.value.filter((item) => item.id !== id)
  persist()
  if (!items.value.some(isInFlight)) {
    stopPoll()
  }
}

export function clearCompletedClipExports(): void {
  items.value = items.value.filter(isInFlight)
  persist()
}

export function findExportByQuery(query: string): ClipExportItem | undefined {
  return items.value.find((item) => item.query === query)
}

export function useClipExports() {
  initClipExports()
  const activeCount = computed(() => items.value.filter(isInFlight).length)
  const uncollectedReadyCount = computed(
    () => items.value.filter((item) => item.status === 'ready' && !item.downloaded).length,
  )
  const hasCompleted = computed(() => items.value.some((item) => !isInFlight(item)))
  return {
    items,
    activeCount,
    uncollectedReadyCount,
    hasCompleted,
    startClipExport,
    retryClipExport,
    triggerSavedClipDownload,
    removeClipExport,
    clearCompletedClipExports,
    findExportByQuery,
  }
}

/** Testing hook: overrides how browser downloads are triggered. */
export function setClipDownloadTrigger(fn: ((url: string, filename: string) => void) | null): void {
  downloadHandler = fn ?? defaultDownloadTrigger
}

/** Testing hook: resets in-memory state and timers between test cases. */
export function resetClipExportsForTest(): void {
  stopPoll()
  pollingInFlight = false
  hydrated = false
  items.value = []
  downloadHandler = defaultDownloadTrigger
}

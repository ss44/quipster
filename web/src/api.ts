export interface CueDTO {
  idx: number
  start: number
  end: number
  text: string
}

export interface Hit {
  chunkId: string
  text: string
  textMarked: string
  mediaId: number
  mediaTitle: string
  showTitle: string
  season: number
  episode: number
  year: number
  startSec: number
  endSec: number
  cues: CueDTO[]
  contextBefore: CueDTO[]
  contextAfter: CueDTO[]
  thumbUrl: string
  /** Meilisearch ranking score (0..1); meaningful for semantic matches. */
  score?: number
}

export interface TitleMatch {
  mediaId: number
  title: string
  showTitle: string
  season: number
  episode: number
  year: number
  status: string
  thumbUrl: string
  noSubtitle: boolean
}

/** Search retrieval mode; '' lets the server pick (hybrid when available). */
export type SearchMode = 'keyword' | 'hybrid' | 'semantic' | ''

export interface SearchResponse {
  query: string
  page: number
  perPage: number
  totalHits: number
  totalPages: number
  hits: Hit[]
  titleMatches: TitleMatch[]
  /** Mode actually executed after server-side auto-resolution. */
  mode: string
  semanticAvailable: boolean
}

export interface Quote {
  chunkId: string
  text: string
  mediaId: number
  mediaTitle: string
  showTitle: string
  season: number
  episode: number
  year: number
  startSec: number
  endSec: number
  thumbUrl: string
}

export interface Suggestion {
  text: string
  textMarked: string
}

export interface ChunkDetail {
  chunk: { id: number; mediaFileId: number; startCueIdx: number; endCueIdx: number; text: string }
  media: MediaFile
  cues: CueDTO[]
  contextBefore: CueDTO[]
  contextAfter: CueDTO[]
}

export interface MediaFile {
  id: number
  path: string
  container: string
  durationSec: number
  width: number
  height: number
  videoCodec: string
  audioCodec: string
  title: string
  year: number
  showTitle: string
  season: number
  episode: number
  status: string
  error: string
}

export interface Job {
  id: number
  type: string
  payload: string
  status: string
  priority: number
  attempts: number
  workerId: string
  error: string
  progress: number
  createdAt: number
  startedAt: number
  finishedAt: number
  mediaId: number
  mediaTitle: string
  mediaPath: string
  mediaStatus: string
  mediaCues: number
  mediaChunks: number
}

export interface NodeInfo {
  id: number
  name: string
  status: 'pending' | 'approved' | 'revoked'
  version: string
  lastSeen: number
  jobsDone: number
  jobsFailed: number
  createdAt: number
}

/** A node row in the admin dashboard: identity plus live state. */
export interface NodeView {
  node: NodeInfo
  online: boolean
  currentJobs: Job[]
}

export interface LineStats {
  cues: number
  chunks: number
  chunksIndexed: number
}

export interface AdminSnapshot {
  scan: { id: number; rootPath: string; status: string; filesSeen: number; filesDone: number } | null
  queue: { queued: number; running: number; done: number; error: number; paused: boolean } | null
  mediaCounts: Record<string, number>
  lines: LineStats | null
  cacheBytes: number
  /** Configured clip-cache quota in bytes; 0 means unlimited. */
  cacheMaxBytes: number
  recentJobs: Job[] | null
  runningJobs: Job[] | null
  meiliUp: boolean
}

export interface AdminConfig {
  mediaDir: string
  folders: string[]
  available: string[]
}

export interface Metric {
  updatedAt: number
  value: number
  total?: number
  unit?: string
  reason?: string
  available: boolean
  stale?: boolean
}

export interface LoadMetric {
  reason?: string
  one: number
  five: number
  fifteen: number
  available: boolean
}

export interface DiskMetric {
  path: string
  name: string
  reason?: string
  freeBytes: number
  totalBytes: number
  available: boolean
}

export interface HardwareStats {
  lastFallbackNote: string
  hwEncodes: number
  cpuRenders: number
  fallbacks: number
  lastFallbackAt: number
}

export interface HardwareStatus {
  requested: string
  backend: string
  device: string
  encoder: string
  decoder: string
  available: boolean
  reason: string
  stats: HardwareStats
}

export interface GaugeSnapshot {
  at: number
  cacheParts: Record<string, number>
  disks: Record<string, DiskMetric>
  cpu: Metric
  processCpu: Metric
  ram: Metric
  swap: Metric
  load: LoadMetric
  gpu: Metric
  vram: Metric
  hardware: HardwareStatus
  cacheBytes: number
  queue?: { queued: number; running: number; done: number; error: number; paused: boolean }
}

export interface VersionInfo {
  version: string
  reason?: string
  available: boolean
}

export interface GPUSummary {
  name: string
  driverVersion: string
  reason?: string
  memoryTotalBytes: number
  memoryUsedBytes: number
  utilizationPct: number
  available: boolean
}

export interface PathInfo {
  name: string
  path: string
  reason?: string
  freeBytes: number
  totalBytes: number
  available: boolean
}

export interface SystemInfo {
  generatedAt: number
  config: Record<string, string>
  hardware: HardwareStatus
  gpu: GPUSummary
  osName: string
  kernel: string
  appVersion: string
  goVersion: string
  goarch: string
  goos: string
  cpuModel: string
  ffmpeg: VersionInfo
  ffprobe: VersionInfo
  paths: PathInfo[]
  hwaccels: string[]
  encoders: string[]
  decoders: string[]
  cpuCores: number
  appUptimeSec: number
  dbBytes: number
  meiliUp: boolean
  container: boolean
}

export interface MediaOptimizationStats {
  updatedAt: number
  scanned: number
  recommended: number
  codes: Record<string, number>
}

export interface JobBench {
  type: string
  count: number
  p50Ms: number
  p95Ms: number
  maxMs: number
  avgWaitMs: number
}

export interface Benchmarks {
  at: number
  window: string
  since: number
  readReason?: string
  jobs: JobBench[]
  throughputPerMin: number
  readMBps: number
  readAvailable: boolean
}


async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) {
    let msg = res.statusText
    try {
      const body = await res.json()
      if (body?.error) msg = body.error
    } catch {
      /* ignore */
    }
    throw new Error(msg)
  }
  return res.json()
}

export const api = {
  examples: () => getJSON<{ examples: string[] }>('/api/examples'),
  quotes: (limit = 4) => getJSON<{ quotes: Quote[] }>(`/api/quotes?limit=${limit}`),
  suggest: (q: string) =>
    getJSON<{ suggestions: Suggestion[] }>(`/api/suggest?q=${encodeURIComponent(q)}`),
  search: (q: string, show: string, page: number, mode: SearchMode = '') =>
    getJSON<SearchResponse>(
      `/api/search?q=${encodeURIComponent(q)}&show=${encodeURIComponent(show)}&page=${page}&mode=${encodeURIComponent(mode)}`
    ),
  chunk: (id: string) => getJSON<ChunkDetail>(`/api/chunks/${encodeURIComponent(id)}`),
  scan: (root: string) =>
    fetch(`/api/admin/scan${root ? `?root=${encodeURIComponent(root)}` : ''}`, { method: 'POST' }).then((r) => r.json()),
  config: () => getJSON<AdminConfig>('/api/admin/config'),
  saveConfig: (folders: string[]) =>
    fetch('/api/admin/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ folders }),
    }).then(async (r) => {
      if (!r.ok) {
        const body = await r.json().catch(() => ({}))
        throw new Error(body.error || 'failed to save library folders')
      }
      return r.json() as Promise<{ folders: string[] }>
    }),
  reindex: () => fetch('/api/admin/reindex', { method: 'POST' }).then((r) => r.json()),
  reprobe: () => fetch('/api/admin/reprobe', { method: 'POST' }).then((r) => r.json()),
  jobs: () => getJSON<{ jobs: Job[] }>('/api/admin/jobs?limit=100'),
  pauseQueue: () => fetch('/api/admin/queue/pause', { method: 'POST' }).then((r) => r.json()),
  resumeQueue: () => fetch('/api/admin/queue/resume', { method: 'POST' }).then((r) => r.json()),
  stopQueue: () =>
    fetch('/api/admin/queue/stop', { method: 'POST' }).then((r) => r.json()) as Promise<{
      paused: boolean
      cancelled: number
      cleared: number
    }>,
  diagnosticsSystem: () => getJSON<SystemInfo>('/api/admin/diagnostics/system'),
  diagnosticsMetrics: () => getJSON<GaugeSnapshot>('/api/admin/diagnostics/metrics'),
  diagnosticsBenchmarks: (window = '1h') =>
    getJSON<Benchmarks>(`/api/admin/diagnostics/benchmarks?window=${encodeURIComponent(window)}`),
  diagnosticsMediaOptimization: () => getJSON<MediaOptimizationStats>('/api/admin/diagnostics/media-optimization'),
  diagnosticsProbe: () =>
    fetch('/api/admin/diagnostics/probe', { method: 'POST' }).then(async (r) => {
      if (!r.ok) throw new Error('hardware probe failed')
      return r.json() as Promise<{ hardware: HardwareStatus }>
    }),
  diagnosticsReport: async (): Promise<string> => {
    const res = await fetch('/api/admin/diagnostics/report', { method: 'POST' })
    if (!res.ok) throw new Error('failed to build diagnostics report')
    return res.text()
  },
  diagnosticsBundleURL: (includePaths = false) =>
    `/api/admin/diagnostics/bundle${includePaths ? '?paths=1' : ''}`,
  logs: (limit = 500) =>
    getJSON<{ lines: string[]; count: number }>(`/api/admin/logs?limit=${limit}`),
  nodes: () => getJSON<{ nodes: NodeView[] }>('/api/admin/nodes'),
  approveNode: async (id: number) => {
    const res = await fetch(`/api/admin/nodes/${id}/approve`, { method: 'POST' })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error(body.error || 'failed to approve node')
    }
    return res.json() as Promise<{ approved: boolean }>
  },
  revokeNode: async (id: number) => {
    const res = await fetch(`/api/admin/nodes/${id}/revoke`, { method: 'POST' })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error(body.error || 'failed to revoke node')
    }
    return res.json() as Promise<{ revoked: boolean }>
  },
  deleteNode: async (id: number) => {
    const res = await fetch(`/api/admin/nodes/${id}`, { method: 'DELETE' })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error(body.error || 'failed to delete node')
    }
    return res.json() as Promise<{ deleted: boolean }>
  },
}

export const diagnosticsStreamURL = '/api/admin/diagnostics/stream'

export function clipStreamURL(hit: Hit, start: number, end: number, mode: string, download: boolean): string {
  const p = new URLSearchParams({
    mediaId: String(hit.mediaId),
    start: start.toFixed(3),
    end: end.toFixed(3),
    mode,
    padMs: '250',
  })
  if (download) p.set('download', '1')
  return `/api/clips/stream?${p.toString()}`
}
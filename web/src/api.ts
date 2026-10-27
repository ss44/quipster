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

export interface SearchResponse {
  query: string
  page: number
  perPage: number
  totalHits: number
  totalPages: number
  hits: Hit[]
  titleMatches: TitleMatch[]
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
  recentJobs: Job[] | null
  runningJobs: Job[] | null
  meiliUp: boolean
}

export interface AdminConfig {
  mediaDir: string
  folders: string[]
  available: string[]
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
  suggest: (q: string) =>
    getJSON<{ suggestions: Suggestion[] }>(`/api/suggest?q=${encodeURIComponent(q)}`),
  search: (q: string, show: string, page: number) =>
    getJSON<SearchResponse>(
      `/api/search?q=${encodeURIComponent(q)}&show=${encodeURIComponent(show)}&page=${page}`
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
}

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
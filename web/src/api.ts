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
  chunk: { id: number; media_file_id: number; start_cue_idx: number; end_cue_idx: number; text: string }
  media: MediaFile
  cues: CueDTO[]
  contextBefore: CueDTO[]
  contextAfter: CueDTO[]
}

export interface MediaFile {
  ID: number
  Path: string
  Container: string
  SizeBytes: number
  DurationSec: number
  Width: number
  Height: number
  VideoCodec: string
  AudioCodec: string
  Title: string
  Year: number
  ShowTitle: string
  Season: number
  Episode: number
  Status: string
  Error: string
}

export interface Job {
  id: number
  type: string
  payload: string
  status: string
  priority: number
  attempts: number
  worker_id: string
  error: string
  progress: number
  created_at: number
  started_at: number
  finished_at: number
}

export interface AdminSnapshot {
  scan: { id: number; root_path: string; status: string; files_seen: number; files_done: number } | null
  queue: { queued: number; running: number; done: number; error: number } | null
  mediaCounts: Record<string, number>
  cacheBytes: number
  recentJobs: Job[] | null
  meiliUp: boolean
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
  reindex: () => fetch('/api/admin/reindex', { method: 'POST' }).then((r) => r.json()),
  jobs: () => getJSON<{ jobs: Job[] }>('/api/admin/jobs?limit=100'),
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
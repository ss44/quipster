import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api'
import {
  CLIP_EXPORTS_STORAGE_KEY,
  clearCompletedClipExports,
  initClipExports,
  pollInFlight,
  removeClipExport,
  resetClipExportsForTest,
  retryClipExport,
  setClipDownloadTrigger,
  startClipExport,
  triggerSavedClipDownload,
  useClipExports,
} from './clipDownloads'

function createMemoryStorage(): Storage {
  const store = new Map<string, string>()
  return {
    get length() {
      return store.size
    },
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => {
      store.delete(key)
    },
    setItem: (key: string, value: string) => {
      store.set(key, String(value))
    },
  }
}

describe('clipDownloads store', () => {
  const triggerSpy = vi.fn()

  beforeEach(() => {
    vi.useFakeTimers()
    vi.stubGlobal('localStorage', createMemoryStorage())
    resetClipExportsForTest()
    setClipDownloadTrigger(triggerSpy)
    triggerSpy.mockReset()
  })

  afterEach(() => {
    resetClipExportsForTest()
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('downloads immediately when the server reports an existing cache hit', async () => {
    vi.spyOn(api, 'createClipJob').mockResolvedValue({
      status: 'ready',
      cacheKey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      cached: true,
      filename: 'Sample Movie [10000-15000].gif',
      downloadUrl: '/api/clips/stream?mediaId=7&mode=gif&download=1',
    })

    const item = await startClipExport({
      hit: { mediaId: 7 },
      title: 'Sample Movie (2020)',
      quote: 'hello world',
      start: 10,
      end: 15,
      mode: 'gif',
      padMs: 250,
    })

    expect(item.status).toBe('ready')
    expect(item.downloaded).toBe(true)
    expect(triggerSpy).toHaveBeenCalledWith(
      '/api/clips/stream?mediaId=7&mode=gif&download=1',
      'Sample Movie [10000-15000].gif',
    )

    const saved = JSON.parse(window.localStorage.getItem(CLIP_EXPORTS_STORAGE_KEY) || '[]')
    expect(saved).toHaveLength(1)
    expect(saved[0].cacheKey).toBe('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')
  })

  it('polls queued exports in the background and auto-downloads on completion', async () => {
    vi.spyOn(api, 'createClipJob').mockResolvedValue({
      status: 'queued',
      jobId: 42,
      cacheKey: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
      cached: false,
      filename: 'Sample Movie [10000-20000].mp4',
      downloadUrl: '/api/clips/stream?mediaId=7&mode=accurate&download=1',
    })
    const statusSpy = vi
      .spyOn(api, 'clipJobStatus')
      .mockResolvedValueOnce({
        jobId: 42,
        status: 'running',
        progress: 0.45,
        error: '',
        cacheKey: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
        cached: false,
      })
      .mockResolvedValueOnce({
        jobId: 42,
        status: 'done',
        progress: 1,
        error: '',
        cacheKey: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
        cached: true,
        filename: 'Sample Movie [10000-20000].mp4',
      })

    const store = useClipExports()
    await startClipExport({
      hit: { mediaId: 7 },
      title: 'Sample Movie',
      quote: 'hello world',
      start: 10,
      end: 20,
      mode: 'accurate',
      padMs: 250,
    })

    expect(store.activeCount.value).toBe(1)
    expect(triggerSpy).not.toHaveBeenCalled()

    // First poll: running at 45%.
    await vi.advanceTimersByTimeAsync(1000)
    expect(statusSpy).toHaveBeenCalledTimes(1)
    expect(store.items.value[0].status).toBe('running')
    expect(store.items.value[0].progress).toBeCloseTo(0.45)

    // Second poll: done & cached -> auto-downloads once.
    await vi.advanceTimersByTimeAsync(1000)
    expect(statusSpy).toHaveBeenCalledTimes(2)
    expect(store.items.value[0].status).toBe('ready')
    expect(store.activeCount.value).toBe(0)
    expect(triggerSpy).toHaveBeenCalledTimes(1)
  })

  it('hydrates persisted queued jobs on reload, marks finished clips ready without unsolicited download, and allows manual download', async () => {
    window.localStorage.setItem(
      CLIP_EXPORTS_STORAGE_KEY,
      JSON.stringify([
        {
          id: 'cccccccccccccccccccccccccccccccc',
          cacheKey: 'cccccccccccccccccccccccccccccccc',
          jobId: 99,
          mediaId: 7,
          title: 'Show S01E01',
          quote: 'a memorable line',
          mode: 'accurate',
          start: 5,
          end: 12,
          padMs: 250,
          hasSubtitles: true,
          query: 'mediaId=7&start=5.000&end=12.000&mode=accurate&padMs=250',
          downloadUrl: '/api/clips/stream?mediaId=7&mode=accurate&download=1',
          filename: 'Show S01E01 [5000-12000].mp4',
          status: 'running',
          progress: 0.3,
          error: '',
          createdAt: 1700000000000,
          completedAt: null,
          downloaded: false,
          autoDownload: true,
        },
      ]),
    )

    vi.spyOn(api, 'clipJobStatus').mockResolvedValue({
      jobId: 99,
      status: 'done',
      progress: 1,
      error: '',
      cacheKey: 'cccccccccccccccccccccccccccccccc',
      cached: true,
      filename: 'Show S01E01 [5000-12000].mp4',
    })

    const store = useClipExports()
    await pollInFlight()

    expect(store.items.value[0].status).toBe('ready')
    expect(store.uncollectedReadyCount.value).toBe(1)
    // Returning users should click Download in Recent Clips rather than getting a surprise pop-up.
    expect(triggerSpy).not.toHaveBeenCalled()

    expect(triggerSavedClipDownload('cccccccccccccccccccccccccccccccc')).toBe(true)
    expect(store.uncollectedReadyCount.value).toBe(0)
    expect(triggerSpy).toHaveBeenCalledTimes(1)
  })

  it('marks evicted ready clips as expired on reload and supports one-click re-render', async () => {
    window.localStorage.setItem(
      CLIP_EXPORTS_STORAGE_KEY,
      JSON.stringify([
        {
          id: 'dddddddddddddddddddddddddddddddd',
          cacheKey: 'dddddddddddddddddddddddddddddddd',
          jobId: null,
          mediaId: 7,
          title: 'Old Clip',
          quote: 'evicted quote',
          mode: 'gif',
          start: 1,
          end: 4,
          padMs: 250,
          hasSubtitles: false,
          query: 'mediaId=7&start=1.000&end=4.000&mode=gif&padMs=250',
          downloadUrl: '/api/clips/stream?mediaId=7&mode=gif&download=1',
          filename: 'Old Clip [1000-4000].gif',
          status: 'ready',
          progress: 1,
          error: '',
          createdAt: 1700000000000,
          completedAt: 1700000005000,
          downloaded: true,
          autoDownload: false,
        },
      ]),
    )
    vi.spyOn(api, 'clipCacheStatus').mockResolvedValue({
      cacheKey: 'dddddddddddddddddddddddddddddddd',
      cached: false,
    })

    initClipExports()
    await Promise.resolve()
    await Promise.resolve()

    const store = useClipExports()
    expect(store.items.value[0].status).toBe('expired')

    vi.spyOn(api, 'createClipJob').mockResolvedValue({
      status: 'queued',
      jobId: 101,
      cacheKey: 'dddddddddddddddddddddddddddddddd',
      cached: false,
      filename: 'Old Clip [1000-4000].gif',
      downloadUrl: '/api/clips/stream?mediaId=7&mode=gif&download=1',
    })

    const retried = await retryClipExport('dddddddddddddddddddddddddddddddd')
    expect(retried?.status).toBe('queued')
    expect(retried?.jobId).toBe(101)

    removeClipExport('dddddddddddddddddddddddddddddddd')
    expect(store.items.value).toHaveLength(0)
    clearCompletedClipExports()
  })
})

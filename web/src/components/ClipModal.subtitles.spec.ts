import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VBtn, VBtnToggle, VDialog, VProgressLinear, VRangeSlider } from 'vuetify/components'
import ClipModal from './ClipModal.vue'
import { api, type ChunkDetail, type Hit } from '../api'
import { resetClipExportsForTest, setClipDownloadTrigger } from '../lib/clipDownloads'

const hit: Hit = {
  chunkId: '7_0-1',
  text: 'hello world',
  textMarked: 'hello world',
  mediaId: 7,
  mediaTitle: 'Sample Movie',
  showTitle: '',
  season: 0,
  episode: 0,
  year: 2020,
  startSec: 10,
  endSec: 16,
  cues: [
    { idx: 0, start: 10, end: 13, text: 'First subtitle line' },
    { idx: 1, start: 13.5, end: 16, text: 'Second subtitle line' },
  ],
  contextBefore: [],
  contextAfter: [],
  thumbUrl: '/api/thumb/7',
}

const detail: ChunkDetail = {
  chunk: { id: 1, mediaFileId: 7, startCueIdx: 0, endCueIdx: 1, text: 'hello world' },
  media: {
    id: 7,
    path: '/media/sample.mkv',
    container: 'mkv',
    durationSec: 600,
    width: 1920,
    height: 1080,
    videoCodec: 'h264',
    audioCodec: 'aac',
    title: 'Sample Movie',
    year: 2020,
    showTitle: '',
    season: 0,
    episode: 0,
    status: 'ready',
    error: '',
  },
  cues: hit.cues,
  contextBefore: [],
  contextAfter: [],
  clipLimits: { maxClipSec: 120, maxGifSec: 10, defaultPadMs: 250, gifRes: '480:-2', gifFps: 12 },
}

function qs<T extends Element>(selector: string): T | null {
  return document.body.querySelector<T>(selector)
}

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

describe('ClipModal embedded subtitles editor', () => {
  let wrapper: VueWrapper

  beforeEach(async () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    vi.spyOn(api, 'chunk').mockResolvedValue(detail)
    const vuetify = createVuetify({
      components: { VDialog, VRangeSlider, VBtn, VBtnToggle, VProgressLinear },
    })
    wrapper = mount(ClipModal, {
      attachTo: document.body,
      global: { plugins: [vuetify] },
    })
    window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
    await flushPromises()
  })

  afterEach(() => {
    wrapper?.unmount()
    document.body.innerHTML = ''
    resetClipExportsForTest()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('renders collapsible subtitle editor under transcript, previews live without reloading video, and exports with params', async () => {
    const initialVideo = qs<HTMLVideoElement>('video')!
    const initialSrc = initialVideo.getAttribute('src')

    // Panel starts collapsed with transcript expanded, burn-in off, and no live overlay.
    expect(qs('#clip-transcript-body')).not.toBeNull()
    expect(qs('#subtitle-style-body')).toBeNull()
    expect(qs('.sub-overlay-text')).toBeNull()

    // Clicking the Subtitles header opens the editor and collapses the transcript.
    qs<HTMLButtonElement>('.sub-collapse-btn')!.click()
    await flushPromises()
    expect(qs('#subtitle-style-body')).not.toBeNull()
    expect(qs('#clip-transcript-body')).toBeNull()

    // Clicking the Transcript header re-expands the transcript and collapses Subtitles.
    qs<HTMLButtonElement>('.transcript-toggle')!.click()
    await flushPromises()
    expect(qs('#clip-transcript-body')).not.toBeNull()
    expect(qs('#subtitle-style-body')).toBeNull()

    // Enabling burn-in auto-expands the editor, collapses the transcript,
    // switches Fast cut -> Precise, and displays the live subtitle overlay
    // without reloading the preview clip.
    const toggle = qs<HTMLInputElement>('input[aria-label="Embed subtitles"]')!
    toggle.checked = true
    toggle.dispatchEvent(new Event('change'))
    await flushPromises()

    expect(qs('#subtitle-style-body')).not.toBeNull()
    expect(qs('#clip-transcript-body')).toBeNull()
    expect(qs('.sub-overlay-text')?.textContent).toBe('First subtitle line')
    expect(qs<HTMLVideoElement>('video')).toBe(initialVideo)
    expect(qs<HTMLVideoElement>('video')!.getAttribute('src')).toBe(initialSrc)
    expect(qs('.hint')?.textContent).toContain('Burned-in subtitles require re-encoding')

    // Change font, font size, and toggle ALL CAPS.
    const fontSelect = qs<HTMLSelectElement>('select[aria-label="Subtitle font"]')!
    fontSelect.value = 'oswald'
    fontSelect.dispatchEvent(new Event('change'))

    const sizeInput = qs<HTMLInputElement>('input[aria-label="Subtitle font size"]')!
    sizeInput.value = '56'
    sizeInput.dispatchEvent(new Event('input'))

    const capsBtn = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.fmt-btn'))
      .find((b) => b.textContent?.trim() === 'AA')!
    capsBtn.click()
    await flushPromises()

    expect(qs('.sub-overlay-text')?.textContent).toBe('FIRST SUBTITLE LINE')
    expect(qs<HTMLElement>('.sub-overlay-text')?.style.fontFamily).toContain('Oswald')

    // Syncing video timeupdate switches the visible overlay to the second cue.
    Object.defineProperty(initialVideo, 'currentTime', { configurable: true, value: 4.5 })
    initialVideo.dispatchEvent(new Event('timeupdate'))
    await flushPromises()
    expect(qs('.sub-overlay-text')?.textContent).toBe('SECOND SUBTITLE LINE')

    // Download and Copy link include subtitle parameters and use accurate mode.
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const downloadTrigger = vi.fn()
    setClipDownloadTrigger(downloadTrigger)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({
        status: 'queued',
        jobId: 77,
        cacheKey: '22222222222222222222222222222222',
        cached: false,
        filename: 'Sample Movie [10000-16000].mp4',
        downloadUrl: '/api/clips/stream?mediaId=7&start=10.000&end=16.000&mode=accurate&subs=1&download=1',
      }),
    })
    vi.stubGlobal('fetch', fetchMock)

    qs<HTMLButtonElement>('.btn-secondary')!.click()
    qs<HTMLButtonElement>('.btn-primary')!.click()
    await flushPromises()

    const copied = new URL(writeText.mock.calls[0][0]).searchParams
    expect(copied.get('mode')).toBe('accurate')
    expect(copied.get('subs')).toBe('1')
    expect(copied.get('subFont')).toBe('oswald')
    expect(copied.get('subSize')).toBe('56')
    expect(copied.get('subUpper')).toBe('1')

    const [jobUrl, jobInit] = fetchMock.mock.calls[0]
    expect(jobInit?.method).toBe('POST')
    const submitted = new URL(jobUrl, 'http://localhost')
    expect(submitted.pathname).toBe('/api/clips/jobs')
    expect(submitted.searchParams.get('mode')).toBe('accurate')
    expect(submitted.searchParams.get('subs')).toBe('1')
    expect(qs('.progress-note')?.textContent).toContain('Rendering in background')

    // Preferences persist in localStorage.
    const saved = JSON.parse(window.localStorage.getItem('quipster.subtitleStyle.v1') || '{}')
    expect(saved.enabled).toBe(true)
    expect(saved.font).toBe('oswald')
    expect(saved.fontSize).toBe(56)
    expect(saved.uppercase).toBe(true)
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VBtn, VBtnToggle, VDialog, VProgressLinear, VRangeSlider } from 'vuetify/components'
import ClipModal from './ClipModal.vue'
import { api, type ChunkDetail, type Hit } from '../api'
import { resetClipExportsForTest, setClipDownloadTrigger } from '../lib/clipDownloads'

// MediaError codes from the HTMLMediaElement spec, mirroring the component.
const MEDIA_ERR_ABORTED = 1
const MEDIA_ERR_SRC_NOT_SUPPORTED = 4

const PREVIEW_WARNING = 'Preview could not be played in this browser. Try downloading instead.'

const hit: Hit = {
  chunkId: '7_0-2',
  text: 'hello world',
  textMarked: 'hello world',
  mediaId: 7,
  mediaTitle: 'Sample Movie',
  showTitle: '',
  season: 0,
  episode: 0,
  year: 2020,
  startSec: 10,
  endSec: 20,
  cues: [
    { idx: 0, start: 10, end: 15, text: 'first line' },
    { idx: 1, start: 20, end: 25, text: 'second line' },
    { idx: 2, start: 30, end: 35, text: 'third line' },
  ],
  contextBefore: [],
  contextAfter: [],
  thumbUrl: '/api/thumb/7',
}

const detail: ChunkDetail = {
  chunk: { id: 1, mediaFileId: 7, startCueIdx: 0, endCueIdx: 2, text: 'hello world' },
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
}

// Fires a media element event the way the browser does, optionally attaching a
// MediaError first, since happy-dom does not emulate media loading itself.
// happy-dom exposes `error` as a getter, so the property is redefined instead
// of assigned.
function fireMediaEvent(el: Element, type: 'error' | 'loadeddata', code?: number) {
  if (code !== undefined) {
    Object.defineProperty(el, 'error', {
      configurable: true,
      value: { code },
    })
  }
  el.dispatchEvent(new Event(type))
}

// The dialog teleports its content to document.body, so the wrapper is
// attached and assertions run against the real document.
function qs<T extends Element>(selector: string): T | null {
  return document.body.querySelector<T>(selector)
}

function warningText(): string {
  return qs('.error-msg')?.textContent?.trim() ?? ''
}

function transcriptLines(): HTMLElement[] {
  return Array.from(document.body.querySelectorAll<HTMLElement>('.transcript .line'))
}

describe('ClipModal preview warning (issue #32)', () => {
  let wrapper: VueWrapper

  beforeEach(async () => {
    vi.spyOn(api, 'chunk').mockResolvedValue(detail)
    // Only the components the modal uses are registered, without stylesheet
    // imports, so tests focus on behaviour rather than styling.
    const vuetify = createVuetify({
      components: { VDialog, VRangeSlider, VBtn, VBtnToggle, VProgressLinear },
    })
    wrapper = mount(ClipModal, {
      attachTo: document.body,
      global: { plugins: [vuetify] },
    })
    // Open the modal the way SearchView does: via the window event.
    window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
    await flushPromises()
    expect(qs<HTMLVideoElement>('video')).not.toBeNull()
  })

  afterEach(() => {
    // Guarded: if beforeEach failed, there is nothing mounted to unmount and
    // the original failure must not be masked by a teardown error.
    wrapper?.unmount()
    document.body.innerHTML = ''
  })

  it('hides native controls until the preview has loaded', async () => {
    const video = qs<HTMLVideoElement>('video')!
    expect(video.hasAttribute('controls')).toBe(false)
    expect(qs('.preview-overlay')).not.toBeNull()

    fireMediaEvent(video, 'loadeddata')
    await wrapper.vm.$nextTick()

    expect(video.hasAttribute('controls')).toBe(true)
    expect(qs('.preview-overlay')).toBeNull()
  })

  it('shows the warning only for a genuine, unrecoverable playback failure', async () => {
    const video = qs<HTMLVideoElement>('video')!
    fireMediaEvent(video, 'error', MEDIA_ERR_SRC_NOT_SUPPORTED)
    await wrapper.vm.$nextTick()
    expect(warningText()).toBe(PREVIEW_WARNING)
    // The loading haze must not stay up for a preview that failed for good.
    expect(qs('.preview-overlay')).toBeNull()
  })

  it('ignores MEDIA_ERR_ABORTED raised while the preview URL is replaced', async () => {
    const video = qs<HTMLVideoElement>('video')!
    fireMediaEvent(video, 'error', MEDIA_ERR_ABORTED)
    await wrapper.vm.$nextTick()
    expect(qs('.error-msg')).toBeNull()
  })

  it('ignores errors from a replaced preview and keeps the haze for the new one', async () => {
    const staleVideo = qs<HTMLVideoElement>('video')!
    const staleSrc = staleVideo.getAttribute('src')

    // Trim the range so updatePreview swaps in a new preview URL (and element).
    transcriptLines()[2].click()
    await flushPromises()

    const freshVideo = qs<HTMLVideoElement>('video')!
    expect(freshVideo).not.toBe(staleVideo)
    expect(freshVideo.getAttribute('src')).not.toBe(staleSrc)

    // The aborted load of the discarded element reports an error after the new
    // preview has already been queued; it must not flag the visible preview.
    fireMediaEvent(staleVideo, 'error', MEDIA_ERR_ABORTED)
    fireMediaEvent(staleVideo, 'error', MEDIA_ERR_SRC_NOT_SUPPORTED)
    await wrapper.vm.$nextTick()
    expect(qs('.error-msg')).toBeNull()
    // The new preview is still loading, so the haze must stay up.
    expect(qs('.preview-overlay')).not.toBeNull()

    fireMediaEvent(freshVideo, 'loadeddata')
    await wrapper.vm.$nextTick()
    expect(qs('.error-msg')).toBeNull()
    expect(qs('.preview-overlay')).toBeNull()
  })

  it('clears a previously shown warning once a later preview loads', async () => {
    fireMediaEvent(qs<HTMLVideoElement>('video')!, 'error', MEDIA_ERR_SRC_NOT_SUPPORTED)
    await wrapper.vm.$nextTick()
    expect(warningText()).toBe(PREVIEW_WARNING)

    // Trim to queue another preview, then let it load successfully.
    transcriptLines()[1].click()
    await flushPromises()
    fireMediaEvent(qs<HTMLVideoElement>('video')!, 'loadeddata')
    await wrapper.vm.$nextTick()

    expect(qs('.error-msg')).toBeNull()
  })

  it('resets the warning when a new clip is opened', async () => {
    fireMediaEvent(qs<HTMLVideoElement>('video')!, 'error', MEDIA_ERR_SRC_NOT_SUPPORTED)
    await wrapper.vm.$nextTick()
    expect(warningText()).toBe(PREVIEW_WARNING)

    window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
    await flushPromises()
    expect(qs('.error-msg')).toBeNull()
  })

  it('clears previous detail and shows haze until a newly opened clip resolves', async () => {
    let resolve!: (value: ChunkDetail) => void
    vi.mocked(api.chunk).mockReturnValueOnce(new Promise(done => { resolve = done }))
    window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
    await flushPromises()
    expect(qs('[aria-label="Loading clip title"]')).not.toBeNull()
    expect(qs('.clip-path')).toBeNull()
    expect(qs('video')).toBeNull()
    expect(qs('.transcript button')).toBeNull()
    expect(document.querySelectorAll('[aria-label="Loading subtitle line"]')).toHaveLength(4)
    expect(document.querySelectorAll('.line-pending > .haze-text')).toHaveLength(4)
    resolve(detail)
    await flushPromises()
    expect(qs('[aria-label="Loading clip title"]')).toBeNull()
    expect(qs('.clip-path')?.textContent).toBe('/media/sample.mkv')
    expect(qs('[aria-label="Preparing preview"]')).not.toBeNull()
  })

  it('shows chunk failures in the dialog instead of an endless haze', async () => {
    vi.mocked(api.chunk).mockRejectedValueOnce(new Error('Clip details unavailable'))
    window.dispatchEvent(new CustomEvent('open-clip', { detail: hit }))
    await flushPromises()
    expect(qs('.haze-text')).toBeNull()
    expect(qs('[role="alert"]')?.textContent).toContain('Clip details unavailable')
    expect(qs('.close-btn')).not.toBeNull()
  })
})

describe('ClipModal GIF export', () => {
  let wrapper: VueWrapper

  beforeEach(async () => {
    vi.spyOn(api, 'chunk').mockResolvedValue({
      ...detail,
      clipLimits: { maxClipSec: 120, maxGifSec: 10, defaultPadMs: 250, gifRes: '480:-2', gifFps: 12 },
    })
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

  function selectOutput(value: 'fast' | 'accurate' | 'gif') {
    const toggle = wrapper.findComponent(VBtnToggle)
    toggle.vm.$emit('update:modelValue', value)
  }

  it('blocks GIF export over 10s without reloading preview, then enables once trimmed within limit', async () => {
    const initialVideo = qs<HTMLVideoElement>('video')!
    const initialSrc = initialVideo.getAttribute('src')

    // Initial selection spans 10s..35s (25.5s including padding), exceeding 10s.
    selectOutput('gif')
    await wrapper.vm.$nextTick()

    expect(qs<HTMLVideoElement>('video')).toBe(initialVideo)
    expect(qs<HTMLVideoElement>('video')!.getAttribute('src')).toBe(initialSrc)
    expect(qs('.limit-msg')?.textContent).toContain('cannot exceed 10s')
    expect(qs('.hint')?.textContent).toContain('480px wide')
    expect(qs<HTMLButtonElement>('.btn-primary')!.disabled).toBe(true)
    expect(qs<HTMLButtonElement>('.btn-secondary')!.disabled).toBe(true)
    expect(qs<HTMLButtonElement>('.btn-primary')!.textContent?.trim()).toBe('Download GIF')

    // Trimming to a single 5s line (5.5s with padding) satisfies the 10s GIF limit.
    transcriptLines()[0].click()
    await flushPromises()

    expect(qs('.limit-msg')).toBeNull()
    expect(qs<HTMLButtonElement>('.btn-primary')!.disabled).toBe(false)
    expect(qs<HTMLButtonElement>('.btn-secondary')!.disabled).toBe(false)

    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const downloadTrigger = vi.fn()
    setClipDownloadTrigger(downloadTrigger)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        status: 'ready',
        cacheKey: '11111111111111111111111111111111',
        cached: true,
        filename: 'Sample Movie [10000-15000].gif',
        downloadUrl: '/api/clips/stream?mediaId=7&start=10.000&end=15.000&mode=gif&padMs=250&download=1',
      }),
    })
    vi.stubGlobal('fetch', fetchMock)

    qs<HTMLButtonElement>('.btn-secondary')!.click()
    qs<HTMLButtonElement>('.btn-primary')!.click()
    await flushPromises()

    const copied = new URL(writeText.mock.calls[0][0]).searchParams
    expect(copied.get('mode')).toBe('gif')
    expect(copied.get('download')).toBeNull()

    const [jobUrl, jobInit] = fetchMock.mock.calls[0]
    expect(jobInit?.method).toBe('POST')
    const submitted = new URL(jobUrl, 'http://localhost')
    expect(submitted.pathname).toBe('/api/clips/jobs')
    expect(submitted.searchParams.get('mode')).toBe('gif')
    expect(submitted.searchParams.get('padMs')).toBe('250')
    expect(downloadTrigger).toHaveBeenCalledWith(
      '/api/clips/stream?mediaId=7&start=10.000&end=15.000&mode=gif&padMs=250&download=1',
      'Sample Movie [10000-15000].gif',
    )
  })
})

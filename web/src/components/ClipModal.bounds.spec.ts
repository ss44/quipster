import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VBtn, VBtnToggle, VDialog, VProgressLinear, VRangeSlider } from 'vuetify/components'
import ClipModal from './ClipModal.vue'
import { api, type ChunkDetail, type CueDTO, type Hit } from '../api'
import { resetClipExportsForTest, setClipDownloadTrigger } from '../lib/clipDownloads'

const cues: CueDTO[] = [
  { idx: 1, start: 10, end: 14, text: 'Context before' },
  { idx: 2, start: 20, end: 25, text: 'First scene line' },
  { idx: 3, start: 30, end: 35, text: 'Second scene line' },
  { idx: 4, start: 40, end: 44, text: 'Context after' },
]

const detail: ChunkDetail = {
  chunk: { id: 1, mediaFileId: 7, startCueIdx: 2, endCueIdx: 3, text: 'Scene' },
  media: {
    id: 7,
    path: '/media/sample.mkv',
    container: 'mkv',
    durationSec: 120,
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
  cues: [cues[1], cues[2]],
  contextBefore: [cues[0]],
  contextAfter: [cues[3]],
  clipLimits: { maxClipSec: 60, maxGifSec: 10, defaultPadMs: 250, gifRes: '480:-2', gifFps: 12 },
}

const hit: Hit = {
  chunkId: '7_2-3',
  text: 'Scene',
  textMarked: 'Scene',
  mediaId: 7,
  mediaTitle: 'Sample Movie',
  showTitle: '',
  season: 0,
  episode: 0,
  year: 2020,
  startSec: 20,
  endSec: 35,
  cues: detail.cues,
  contextBefore: detail.contextBefore,
  contextAfter: detail.contextAfter,
  thumbUrl: '/api/thumb/7',
}

function qs<T extends Element>(selector: string): T | null {
  return document.body.querySelector<T>(selector)
}

function previewParams(): URLSearchParams {
  const src = qs<HTMLVideoElement>('video')!.getAttribute('src')!
  return new URL(src, 'http://localhost').searchParams
}

describe('ClipModal custom start/end timing controls', () => {
  let wrapper: VueWrapper

  beforeEach(async () => {
    vi.useFakeTimers()
    vi.spyOn(api, 'chunk').mockResolvedValue(structuredClone(detail))
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
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('nudges start and end beyond transcript bounds with padMs=0 and debounces preview reloads', async () => {
    // Initially locked to transcript cues [20, 35] with 250ms padding (effective 19.75..35.25).
    expect(previewParams().get('start')).toBe('20.000')
    expect(previewParams().get('end')).toBe('35.000')
    expect(previewParams().get('padMs')).toBe('250')
    expect(qs<HTMLInputElement>('input[aria-label="Clip start time"]')!.value).toBe('0:19.75')
    expect(qs<HTMLInputElement>('input[aria-label="Clip end time"]')!.value).toBe('0:35.25')
    expect(qs('.custom-pill')).toBeNull()

    // Click -1s twice on Start and +0.5s once on End in quick succession.
    qs<HTMLButtonElement>('button[aria-label="Start 1 second earlier"]')!.click()
    await flushPromises()
    qs<HTMLButtonElement>('button[aria-label="Start 1 second earlier"]')!.click()
    await flushPromises()
    qs<HTMLButtonElement>('button[aria-label="End 0.5 seconds later"]')!.click()
    await flushPromises()

    // Display updates immediately while preview waits for debounce.
    expect(qs('.custom-pill')?.textContent?.trim()).toBe('Custom')
    expect(qs<HTMLInputElement>('input[aria-label="Clip start time"]')!.value).toBe('0:17.75')
    expect(qs<HTMLInputElement>('input[aria-label="Clip end time"]')!.value).toBe('0:35.75')
    expect(qs('.trim-meta')?.textContent).toContain('0:17.75 – 0:35.75')
    expect(qs('.trim-meta')?.textContent).toContain('18.0s')
    expect(previewParams().get('start')).toBe('20.000')

    // Copy link and Download both use the updated custom bounds even before preview debounce fires.
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const downloadTrigger = vi.fn()
    setClipDownloadTrigger(downloadTrigger)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        status: 'ready',
        cacheKey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
        cached: true,
        filename: 'Sample Movie [17750-35750].mp4',
        downloadUrl: '/api/clips/stream?mediaId=7&start=17.750&end=35.750&mode=fast&padMs=0&download=1',
      }),
    })
    vi.stubGlobal('fetch', fetchMock)
    qs<HTMLButtonElement>('.btn-secondary')!.click()
    qs<HTMLButtonElement>('.btn-primary')!.click()
    await flushPromises()
    const copied = new URL(writeText.mock.calls[0][0]).searchParams
    expect(copied.get('start')).toBe('17.750')
    expect(copied.get('end')).toBe('35.750')
    expect(copied.get('padMs')).toBe('0')
    const submitted = new URL(fetchMock.mock.calls[0][0], 'http://localhost')
    expect(submitted.pathname).toBe('/api/clips/jobs')
    expect(submitted.searchParams.get('start')).toBe('17.750')
    expect(submitted.searchParams.get('end')).toBe('35.750')
    expect(submitted.searchParams.get('padMs')).toBe('0')

    // Advancing past debounce reloads the preview once with exact padMs=0 bounds.
    vi.advanceTimersByTime(300)
    await flushPromises()
    expect(previewParams().get('start')).toBe('17.750')
    expect(previewParams().get('end')).toBe('35.750')
    expect(previewParams().get('padMs')).toBe('0')

    // Resetting snaps back to transcript bounds with defaultPadMs.
    const resetBtn = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.timing-panel .link-btn'))
      .find((b) => b.textContent?.includes('Reset to transcript'))!
    resetBtn.click()
    await flushPromises()
    expect(qs('.custom-pill')).toBeNull()
    expect(previewParams().get('start')).toBe('20.000')
    expect(previewParams().get('end')).toBe('35.000')
    expect(previewParams().get('padMs')).toBe('250')
  })

  it('commits typed timestamps, supports playhead snapping, and syncs transcript highlights + GIF eligibility', async () => {
    const startInput = qs<HTMLInputElement>('input[aria-label="Clip start time"]')!
    const endInput = qs<HTMLInputElement>('input[aria-label="Clip end time"]')!

    // Type arbitrary timestamps cutting into the middle of cue 2 and extending into cue 4.
    startInput.focus()
    startInput.value = '0:22.5'
    startInput.dispatchEvent(new Event('input'))
    startInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()

    endInput.focus()
    endInput.value = '0:41.0'
    endInput.dispatchEvent(new Event('input'))
    endInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()

    expect(previewParams().get('start')).toBe('22.500')
    expect(previewParams().get('end')).toBe('41.000')
    expect(previewParams().get('padMs')).toBe('0')

    // Transcript highlights all cues overlapping [22.5, 41.0]: cues 2, 3, and 4 (3 lines).
    const activeLines = Array.from(document.body.querySelectorAll('.transcript .line.active'))
    expect(activeLines).toHaveLength(3)
    expect(qs('.trim-meta')?.textContent).toContain('3 lines')

    // Switch to GIF mode (18.5s > 10s limit -> blocked).
    wrapper.findComponent(VBtnToggle).vm.$emit('update:modelValue', 'gif')
    await flushPromises()
    expect(qs('.limit-msg')?.textContent).toContain('cannot exceed 10s')
    expect(qs<HTMLButtonElement>('.btn-primary')!.disabled).toBe(true)

    // Load preview and seek playhead 7.5s into the clip (media time 22.5 + 7.5 = 30.0s),
    // then click "Set end to current playback time" to trim the clip to 7.5s -> GIF allowed!
    const video = qs<HTMLVideoElement>('video')!
    video.dispatchEvent(new Event('loadeddata'))
    Object.defineProperty(video, 'currentTime', { configurable: true, value: 7.5 })
    video.dispatchEvent(new Event('timeupdate'))
    await flushPromises()

    const setEndBtn = qs<HTMLButtonElement>('button[aria-label="Set end to current playback time"]')!
    expect(setEndBtn.disabled).toBe(false)
    setEndBtn.click()
    await flushPromises()

    expect(previewParams().get('start')).toBe('22.500')
    expect(previewParams().get('end')).toBe('30.000')
    expect(previewParams().get('padMs')).toBe('0')
    expect(qs('.limit-msg')).toBeNull()
    expect(qs<HTMLButtonElement>('.btn-primary')!.disabled).toBe(false)

    // Malformed input restores the current formatted timestamp without mutating bounds.
    startInput.focus()
    startInput.value = 'not-a-time'
    startInput.dispatchEvent(new Event('input'))
    startInput.dispatchEvent(new Event('blur'))
    await flushPromises()
    expect(startInput.value).toBe('0:22.50')
    expect(previewParams().get('start')).toBe('22.500')
  })

  it('supports keyboard ArrowUp/ArrowDown fine stepping and clears custom mode when transcript range changes', async () => {
    const startInput = qs<HTMLInputElement>('input[aria-label="Clip start time"]')!
    startInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp' }))
    await flushPromises()
    expect(startInput.value).toBe('0:19.85')

    startInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', shiftKey: true }))
    await flushPromises()
    expect(startInput.value).toBe('0:18.85')

    // Clicking a transcript line re-locks to transcript timing.
    document.body.querySelectorAll<HTMLButtonElement>('.transcript .line')[0].click()
    await flushPromises()
    expect(qs('.custom-pill')).toBeNull()
    expect(previewParams().get('start')).toBe('10.000')
    expect(previewParams().get('end')).toBe('35.000')
    expect(previewParams().get('padMs')).toBe('250')
  })
})

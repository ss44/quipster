import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VBtn, VBtnToggle, VDialog, VProgressLinear, VRangeSlider } from 'vuetify/components'
import ClipModal from './ClipModal.vue'
import { api, type ChunkDetail, type Hit } from '../api'

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
    wrapper.unmount()
    document.body.innerHTML = ''
  })

  it('shows the warning only for a genuine, unrecoverable playback failure', async () => {
    const video = qs<HTMLVideoElement>('video')!
    fireMediaEvent(video, 'error', MEDIA_ERR_SRC_NOT_SUPPORTED)
    await wrapper.vm.$nextTick()
    expect(warningText()).toBe(PREVIEW_WARNING)
    // The spinner must not stay up for a preview that failed for good.
    expect(qs('.preview-overlay')).toBeNull()
  })

  it('ignores MEDIA_ERR_ABORTED raised while the preview URL is replaced', async () => {
    const video = qs<HTMLVideoElement>('video')!
    fireMediaEvent(video, 'error', MEDIA_ERR_ABORTED)
    await wrapper.vm.$nextTick()
    expect(qs('.error-msg')).toBeNull()
  })

  it('ignores errors from a replaced preview and keeps the spinner for the new one', async () => {
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
    // The new preview is still loading, so the spinner must stay up.
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
})

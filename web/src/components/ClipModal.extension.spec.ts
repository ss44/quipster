import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VBtn, VBtnToggle, VDialog, VProgressLinear, VRangeSlider } from 'vuetify/components'
import ClipModal from './ClipModal.vue'
import { api, type ChunkDetail, type CueDTO, type Hit } from '../api'

const cue = (idx: number): CueDTO => ({ idx, start: idx * 10, end: idx * 10 + 5, text: `Line ${idx}` })
const detail: ChunkDetail = {
  chunk: { id: 1, mediaFileId: 7, startCueIdx: 2, endCueIdx: 3, text: 'Scene' },
  media: { id: 7, path: '/movie.mkv', container: 'mkv', durationSec: 100, width: 1920, height: 1080,
    videoCodec: 'h264', audioCodec: 'aac', title: 'Movie', year: 2020, showTitle: '', season: 0,
    episode: 0, status: 'ready', error: '' },
  cues: [cue(2), cue(3)], contextBefore: [cue(1)], contextAfter: [cue(4)],
}
const hit: Hit = { chunkId: '7_2-3', mediaId: 7, text: 'Scene', textMarked: 'Scene', mediaTitle: 'Movie',
  showTitle: '', season: 0, episode: 0, year: 2020, startSec: 20, endSec: 35,
  cues: detail.cues, contextBefore: detail.contextBefore, contextAfter: detail.contextAfter, thumbUrl: '' }
const button = (direction: 'previous' | 'next') => document.body.querySelector<HTMLButtonElement>(`[aria-label="Include ${direction} subtitle line"]`)!
const activeText = () => Array.from(document.body.querySelectorAll('.line.active')).map(el => el.textContent?.trim())
const preview = () => new URL(document.body.querySelector('video')!.getAttribute('src')!, 'http://localhost').searchParams

async function openClip(value = hit) {
  window.dispatchEvent(new CustomEvent('open-clip', { detail: value }))
  await flushPromises()
}

describe('ClipModal subtitle-line extension', () => {
  let wrapper: VueWrapper
  beforeEach(async () => {
    vi.spyOn(api, 'chunk').mockResolvedValue(structuredClone(detail))
    wrapper = mount(ClipModal, { attachTo: document.body, global: { plugins: [createVuetify({
      components: { VDialog, VRangeSlider, VBtn, VBtnToggle, VProgressLinear },
    })] } })
    await openClip()
  })
  afterEach(() => { wrapper?.unmount(); document.body.innerHTML = ''; vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('fetches one more line beyond context and stops at subtitle boundaries', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ cues: [cue(0)] }) })
    vi.stubGlobal('fetch', fetchMock)
    button('previous').click()
    await flushPromises()
    button('previous').click()
    await flushPromises()
    expect(fetchMock).toHaveBeenCalledWith('/api/media/7/cues?startIdx=0&endIdx=0')
    expect(preview().get('start')).toBe('0.000')
    expect(button('previous').disabled).toBe(true)
    expect(activeText()).toHaveLength(4)
    button('next').click()
    await flushPromises()
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ cues: [] }) })
    button('next').click()
    await flushPromises()
    expect(fetchMock).toHaveBeenLastCalledWith('/api/media/7/cues?startIdx=5&endIdx=5')
    expect(button('next').disabled).toBe(true)
    expect(preview().get('end')).toBe('45.000')
  })

  it.each(['reopen', 'close', 'unmount', 'trim'] as const)('ignores an extension response after %s', async (change) => {
    let finish!: (value: unknown) => void
    const pending = new Promise(resolve => { finish = resolve })
    vi.stubGlobal('fetch', vi.fn(() => pending))
    button('next').click()
    await flushPromises()
    button('next').click()
    await flushPromises()
    expect(button('previous').disabled).toBe(true)
    expect(button('next').disabled).toBe(true)
    if (change === 'reopen') await openClip()
    if (change === 'close') document.body.querySelector<HTMLButtonElement>('[aria-label="Close"]')!.click()
    if (change === 'unmount') wrapper.unmount()
    if (change === 'trim') {
      document.body.querySelectorAll<HTMLButtonElement>('.transcript .line')[1].click()
      await flushPromises()
    }
    finish({ ok: true, json: async () => ({ cues: [cue(5)] }) })
    await flushPromises()
    expect(document.body.textContent).not.toContain('Line 5')
    if (change === 'reopen') {
      expect(preview().get('start')).toBe('20.000')
      expect(preview().get('end')).toBe('35.000')
      expect(button('next').disabled).toBe(false)
    }
    if (change === 'trim') {
      expect(preview().get('start')).toBe('20.000')
      expect(preview().get('end')).toBe('25.000')
    }
  })

  it('defers preview changes until slider end while copy and download use the latest selection', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, statusText: 'Test download', json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)
    const slider = wrapper.findComponent(VRangeSlider)
    const initialSrc = document.body.querySelector('video')!.getAttribute('src')
    for (const range of [[0, 2], [1, 3], [0, 3]]) {
      slider.vm.$emit('update:modelValue', range)
      await flushPromises()
      expect(document.body.querySelector('video')!.getAttribute('src')).toBe(initialSrc)
    }
    document.body.querySelector<HTMLButtonElement>('.btn-secondary')!.click()
    document.body.querySelector<HTMLButtonElement>('.btn-primary')!.click()
    await flushPromises()
    const copy = new URL(writeText.mock.calls[0][0]).searchParams
    const download = new URL(fetchMock.mock.calls[0][0], 'http://localhost').searchParams
    for (const params of [copy, download]) {
      expect(params.get('start')).toBe('10.000')
      expect(params.get('end')).toBe('45.000')
      expect(params.get('mode')).toBe('fast')
    }
    expect(download.get('download')).toBe('1')
    expect(document.body.querySelector('video')!.getAttribute('src')).toBe(initialSrc)
    slider.vm.$emit('end', [0, 3])
    await flushPromises()
    expect(preview().get('start')).toBe('10.000')
    expect(preview().get('end')).toBe('45.000')
    expect(preview().get('mode')).toBe('preview')
  })

  it('clears the prior scene while loading and ignores an older chunk response', async () => {
    let finish!: (value: ChunkDetail) => void
    vi.mocked(api.chunk).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await openClip({ ...hit, chunkId: '8_6-6', mediaId: 8 })
    expect(document.body.querySelector('video')).toBeNull()
    await openClip()
    finish({ ...detail, media: { ...detail.media, id: 8 }, cues: [cue(6)] })
    await flushPromises()
    expect(activeText()).toEqual(['0:20Line 2', '0:30Line 3'])
    expect(preview().get('mediaId')).toBe('7')
  })

  it('disables range actions and reports zero selected lines for an empty scene', async () => {
    vi.mocked(api.chunk).mockResolvedValue({ ...detail, cues: [] })
    await openClip()
    expect(document.body.querySelector('video')).toBeNull()
    expect(document.body.querySelector('.trim-meta')!.textContent).toContain('0 lines')
    expect(button('previous').disabled).toBe(true)
    expect(button('next').disabled).toBe(true)
    expect(document.body.querySelector<HTMLButtonElement>('.btn-primary')!.disabled).toBe(true)
    expect(document.body.querySelector<HTMLButtonElement>('.btn-secondary')!.disabled).toBe(true)
  })

  it('preserves the range on API failure, prevents duplicate requests, and permits retry', async () => {
    let reject!: (error: Error) => void
    const fetchMock = vi.fn().mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail }))
    vi.stubGlobal('fetch', fetchMock)
    button('next').click()
    await flushPromises()
    button('next').click()
    button('next').click()
    await flushPromises()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    reject(new Error('Could not load subtitle line'))
    await flushPromises()
    expect(document.body.querySelector('.error-msg')!.textContent).toBe('Could not load subtitle line')
    expect(preview().get('end')).toBe('45.000')
    expect(button('next').disabled).toBe(false)
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ cues: [cue(5)] }) })
    button('next').click()
    await flushPromises()
    expect(preview().get('end')).toBe('55.000')
    expect(document.body.querySelector('.error-msg')).toBeNull()
  })

  it('resets exhausted boundaries when reopening a scene', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ cues: [] }) }))
    button('next').click()
    await flushPromises()
    button('next').click()
    await flushPromises()
    expect(button('next').disabled).toBe(true)
    await openClip()
    expect(button('next').disabled).toBe(false)
    expect(preview().get('end')).toBe('35.000')
  })

  it('keeps the original scene selected and extends each edge by exactly one context line', async () => {
    expect(activeText()).toEqual(['0:20Line 2', '0:30Line 3'])
    expect(preview().get('start')).toBe('20.000')
    expect(preview().get('end')).toBe('35.000')
    expect(button('previous')).not.toBeNull()
    expect(button('next')).not.toBeNull()
    button('previous').click()
    await flushPromises()
    expect(activeText()).toEqual(['0:10Line 1', '0:20Line 2', '0:30Line 3'])
    expect(preview().get('start')).toBe('10.000')
    expect(preview().get('end')).toBe('35.000')
    button('next').click()
    await flushPromises()
    expect(activeText()).toEqual(['0:10Line 1', '0:20Line 2', '0:30Line 3', '0:40Line 4'])
    expect(preview().get('end')).toBe('45.000')
    expect(document.body.querySelectorAll('.transcript .line')).toHaveLength(4)
  })
})

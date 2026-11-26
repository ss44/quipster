import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ClipExportsTray from './ClipExportsTray.vue'
import { api } from '../../api'
import {
  CLIP_EXPORTS_STORAGE_KEY,
  resetClipExportsForTest,
  setClipDownloadTrigger,
} from '../../lib/clipDownloads'

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

describe('ClipExportsTray', () => {
  const triggerSpy = vi.fn()

  beforeEach(() => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    resetClipExportsForTest()
    setClipDownloadTrigger(triggerSpy)
    triggerSpy.mockReset()
  })

  afterEach(() => {
    resetClipExportsForTest()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('stays hidden when there are no exported clips', () => {
    const wrapper = mount(ClipExportsTray)
    expect(wrapper.find('.exports-trigger').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows recent clips from localStorage, opens the popover, and lets returning users download ready clips', async () => {
    window.localStorage.setItem(
      CLIP_EXPORTS_STORAGE_KEY,
      JSON.stringify([
        {
          id: 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
          cacheKey: 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
          jobId: 12,
          mediaId: 7,
          title: 'Show S01E01',
          quote: 'I am the danger',
          mode: 'gif',
          start: 10,
          end: 14,
          padMs: 250,
          hasSubtitles: true,
          query: 'mediaId=7&start=10.000&end=14.000&mode=gif&padMs=250&subs=1',
          downloadUrl: '/api/clips/stream?mediaId=7&mode=gif&download=1',
          filename: 'Show S01E01 [10000-14000].gif',
          status: 'ready',
          progress: 1,
          error: '',
          createdAt: 1700000000000,
          completedAt: 1700000004000,
          downloaded: false,
          autoDownload: false,
        },
      ]),
    )
    vi.spyOn(api, 'clipCacheStatus').mockResolvedValue({
      cacheKey: 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
      cached: true,
    })

    const wrapper = mount(ClipExportsTray)
    await flushPromises()

    const trigger = wrapper.find('.exports-trigger')
    expect(trigger.exists()).toBe(true)
    expect(trigger.text()).toContain('1')

    await trigger.trigger('click')
    expect(wrapper.find('#clip-exports-popover').exists()).toBe(true)
    expect(wrapper.text()).toContain('Show S01E01')
    expect(wrapper.text()).toContain('GIF')
    expect(wrapper.text()).toContain('SUBS')
    expect(wrapper.text()).toContain('Ready to download')

    await wrapper.find('.export-action.primary').trigger('click')
    expect(triggerSpy).toHaveBeenCalledWith(
      '/api/clips/stream?mediaId=7&mode=gif&download=1',
      'Show S01E01 [10000-14000].gif',
    )

    await wrapper.find('.clear-btn').trigger('click')
    expect(wrapper.find('.exports-trigger').exists()).toBe(false)
    wrapper.unmount()
  })
})

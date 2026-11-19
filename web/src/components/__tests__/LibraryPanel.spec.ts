import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createVuetify } from 'vuetify'
import { VCombobox, VDialog } from 'vuetify/components'
import LibraryPanel from '../LibraryPanel.vue'
import { api, type AdminConfig, type AdminSnapshot, type Job } from '../../api'

vi.mock('../../api', () => ({ api: { config: vi.fn() } }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function snapshot(extra: Partial<AdminSnapshot> = {}): AdminSnapshot {
  return {
    scan: null,
    queue: { queued: 0, running: 0, done: 0, error: 0, paused: false },
    mediaCounts: {},
    lines: { cues: 0, chunks: 0, chunksIndexed: 0 },
    cacheBytes: 0,
    recentJobs: [],
    runningJobs: [],
    meiliUp: true,
    ...extra,
  }
}

const config: AdminConfig = { mediaDir: '/media', folders: [], available: ['movies'], cacheMaxBytes: 0 }
const job: Job = {
  id: 1, type: 'probe', payload: '', status: 'running', priority: 0, attempts: 1,
  workerId: 'local', error: '', progress: 0, createdAt: 1, startedAt: 1, finishedAt: 0,
  mediaId: 1, mediaTitle: 'Known movie', mediaPath: '/media/movie.mkv', mediaStatus: 'pending',
  mediaCues: 0, mediaChunks: 0,
}

class FakeEventSource {
  static instance: FakeEventSource
  onerror: (() => void) | null = null
  onopen: (() => void) | null = null
  close = vi.fn()
  private tick: ((event: MessageEvent) => void) | null = null

  constructor() { FakeEventSource.instance = this }

  addEventListener(type: string, callback: (event: MessageEvent) => void) {
    if (type === 'tick') this.tick = callback
  }

  emit(data: AdminSnapshot) { this.tick?.({ data: JSON.stringify(data) } as MessageEvent) }
}

const vuetify = createVuetify({ components: { VCombobox, VDialog } })
let wrapper: VueWrapper
let configRequest: ReturnType<typeof deferred<AdminConfig>>

beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource)
  configRequest = deferred<AdminConfig>()
  vi.mocked(api.config).mockReturnValue(configRequest.promise)
  wrapper = mount(LibraryPanel, { global: { plugins: [vuetify] } })
})

afterEach(() => {
  wrapper.unmount()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('LibraryPanel loading', () => {
  it('uses hazy text instead of initial status, totals, folders and empty jobs', () => {
    for (const label of ['Loading library status', 'Loading library statistics', 'Loading dialogue statistics',
      'Loading queue statistics', 'Loading clip cache statistics', 'Loading selected folders',
      'Loading library root', 'Loading current processing', 'Loading recent jobs']) {
      expect(wrapper.find(`[role="status"][aria-label="${label}"]`).exists()).toBe(true)
    }
    for (const text of ['Idle', 'Search offline', 'Whole root', 'No dialogue indexed yet',
      'Nothing is being decoded', 'No jobs yet', '0 shown', 'no limit', '0 searchable']) {
      expect(wrapper.text()).not.toContain(text)
    }
    expect(wrapper.findAll('.stat-progress')).toHaveLength(0)
    expect(wrapper.getComponent(VCombobox).props('disabled')).toBe(true)
    expect(wrapper.findAll('button').find((button) => button.text() === 'Scan')!.attributes('disabled')).toBeDefined()
  })

  it('reveals real zero and empty values only after their requests settle', async () => {
    configRequest.resolve(config)
    FakeEventSource.instance.emit(snapshot())
    await flushPromises()

    expect(wrapper.findAll('.haze-text')).toHaveLength(0)
    expect(wrapper.get('.head-status').text()).toContain('Idle')
    expect(wrapper.get('.head-status').text()).toContain('Search online')
    expect(wrapper.get('.folders').text()).toContain('Whole root')
    expect(wrapper.findAll('.stat-value')[1].text()).toBe('0lines')
    expect(wrapper.text()).toContain('No dialogue indexed yet')
    expect(wrapper.text()).toContain('Nothing is being decoded right now.')
    expect(wrapper.text()).toContain('No jobs yet')
    expect(wrapper.getComponent(VCombobox).props('disabled')).toBe(false)
  })

  it('keeps null dialogue totals hazy while allowing known statistics and cache usage', async () => {
    FakeEventSource.instance.emit(snapshot({ lines: null, mediaCounts: { indexed: 7 }, cacheBytes: 1024 }))
    await flushPromises()

    const cards = wrapper.findAll('.stat')
    expect(cards[0].get('.stat-value').text()).toBe('7files')
    expect(cards[1].find('[aria-label="Loading dialogue statistics"]').exists()).toBe(true)
    expect(cards[1].text()).not.toContain('No dialogue indexed yet')
    expect(cards[1].find('.stat-progress').exists()).toBe(false)
    expect(cards[3].text()).toContain('1.0 KB')
    expect(cards[3].find('[aria-label="Loading clip cache progress"]').exists()).toBe(true)
    expect(cards[3].text()).not.toContain('no limit')

    FakeEventSource.instance.emit(snapshot({ lines: { cues: 0, chunks: 0, chunksIndexed: 0 } }))
    await flushPromises()
    expect(cards[1].get('.stat-value').text()).toBe('0lines')
  })

  it('settles configuration failures as unavailable and leaves unsafe controls disabled', async () => {
    configRequest.reject(new Error('Config request failed'))
    FakeEventSource.instance.emit(snapshot())
    await flushPromises()

    expect(wrapper.get('.folders').text()).toContain('Unavailable')
    expect(wrapper.get('.folders').text()).toContain('Config request failed')
    expect(wrapper.get('.folders').text()).not.toContain('Whole root')
    expect(wrapper.get('.folders').find('.haze-text').exists()).toBe(false)
    expect(wrapper.getComponent(VCombobox).props('disabled')).toBe(true)
    for (const text of ['Save folders', 'Scan']) {
      expect(wrapper.findAll('button').find((button) => button.text() === text)!.attributes('disabled')).toBeDefined()
    }
    expect(wrapper.findAll('.stat')[3].text()).toContain('Progress unavailable')
    expect(wrapper.text()).not.toContain('no limit')
  })

  it('replaces initial stream haze with reconnect feedback until a snapshot arrives', async () => {
    configRequest.resolve(config)
    FakeEventSource.instance.onerror?.()
    await flushPromises()

    expect(wrapper.findAll('.haze-text')).toHaveLength(0)
    expect(wrapper.get('.head-status [role="alert"]').text()).toContain('Waiting for reconnect')
    expect(wrapper.get('.activity').text()).toContain('Processing status unavailable')
    expect(wrapper.get('.jobs').text()).toContain('Recent jobs unavailable')
    expect(wrapper.get('.head-status').text()).not.toContain('Idle')
    expect(wrapper.get('.head-status').text()).not.toContain('offline')

    FakeEventSource.instance.onopen?.()
    await flushPromises()
    expect(wrapper.get('.head-status [role="alert"]').text()).toContain('Waiting for reconnect')
    FakeEventSource.instance.emit(snapshot())
    await flushPromises()
    expect(wrapper.find('.head-status [role="alert"]').exists()).toBe(false)
    expect(wrapper.get('.head-status').text()).toContain('Idle')
  })

  it('preserves known data across stream failure and clears errors on an identical tick', async () => {
    configRequest.resolve(config)
    const known = snapshot({ mediaCounts: { indexed: 7 }, runningJobs: [job], recentJobs: [job] })
    FakeEventSource.instance.emit(known)
    await flushPromises()
    FakeEventSource.instance.onerror?.()
    await flushPromises()

    expect(wrapper.findAll('.haze-text')).toHaveLength(0)
    expect(wrapper.findAll('.stat-value')[0].text()).toBe('7files')
    expect(wrapper.get('.activity').text()).toContain('Known movie')
    expect(wrapper.get('.jobs').text()).toContain('Known movie')
    expect(wrapper.get('.head-status [role="alert"]').text()).toContain('Waiting for reconnect')

    FakeEventSource.instance.emit(known)
    await flushPromises()
    expect(wrapper.find('.head-status [role="alert"]').exists()).toBe(false)
  })

  it('closes its stream on unmount', () => {
    wrapper.unmount()
    expect(FakeEventSource.instance.close).toHaveBeenCalledOnce()
  })
})

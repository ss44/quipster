import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createVuetify } from 'vuetify'
import { VPagination } from 'vuetify/components'
import SearchView from '../SearchView.vue'
import { api, type Hit, type Quote, type SearchResponse } from '../../api'

vi.mock('../../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api')>()
  return {
    ...actual,
    api: {
      quotes: vi.fn(),
      examples: vi.fn(),
      search: vi.fn(),
    },
  }
})

const vuetify = createVuetify({ components: { VPagination } })

function hit(extra: Partial<Hit> = {}): Hit {
  return {
    chunkId: '7_0',
    text: 'hello world',
    textMarked: '<b>hello</b> world',
    mediaId: 7,
    mediaTitle: 'Sample Movie',
    showTitle: 'Sample Show',
    season: 1,
    episode: 2,
    year: 2020,
    startSec: 65,
    endSec: 72,
    cues: [],
    contextBefore: [],
    contextAfter: [],
    thumbUrl: '/api/thumb/7',
    ...extra,
  }
}

function quote(extra: Partial<Quote> = {}): Quote {
  return {
    chunkId: '7_0',
    text: 'A memorable line',
    mediaId: 7,
    mediaTitle: 'Sample Movie',
    showTitle: 'Sample Show',
    season: 1,
    episode: 2,
    year: 2020,
    startSec: 65,
    endSec: 72,
    thumbUrl: '/api/thumb/7',
    ...extra,
  }
}

function response(extra: Partial<SearchResponse> = {}): SearchResponse {
  return {
    query: 'hello',
    page: 1,
    perPage: 20,
    totalHits: 1,
    totalPages: 1,
    hits: [hit()],
    titleMatches: [],
    mode: 'keyword',
    semanticAvailable: true,
    ...extra,
  }
}

let wrapper: VueWrapper | null = null

async function mountView(path: string): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/admin', name: 'admin', component: { template: '<div />' } },
      { path: '/search', name: 'search', component: { template: '<div />' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  wrapper = mount(SearchView, {
    attachTo: document.body,
    global: { plugins: [vuetify, router] },
  })
  await flushPromises()
  return router
}

beforeEach(() => {
  vi.mocked(api.quotes).mockResolvedValue({ quotes: [] })
  vi.mocked(api.examples).mockResolvedValue({ examples: [] })
  vi.mocked(api.search).mockReset()
  vi.mocked(api.search).mockResolvedValue(response({ hits: [] }))
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

describe('SearchView landing', () => {
  it('renders library quotes and opens the selected clip', async () => {
    const selected = quote()
    vi.mocked(api.quotes).mockResolvedValue({ quotes: [selected] })
    const opened: CustomEvent[] = []
    const listener = (event: Event) => opened.push(event as CustomEvent)
    window.addEventListener('open-clip', listener)

    await mountView('/')

    expect(wrapper!.text()).toContain('Find any line you remember.')
    expect(wrapper!.text()).toContain('“A memorable line”')
    await wrapper!.find('.quote-card').trigger('click')

    window.removeEventListener('open-clip', listener)
    expect(opened).toHaveLength(1)
    expect(opened[0].detail).toEqual(selected)
    expect(api.search).not.toHaveBeenCalled()
  })

  it('offers example queries when the library has no quotes', async () => {
    vi.mocked(api.examples).mockResolvedValue({ examples: ['what do you mean'] })
    const router = await mountView('/')

    const example = wrapper!.find('.example-row')
    expect(example.text()).toContain('what do you mean')
    await example.trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query.q).toBe('what do you mean')
  })
})

describe('SearchView results', () => {
  it('loads and composes results for the active query', async () => {
    vi.mocked(api.search).mockResolvedValue(response())

    await mountView('/search?q=hello')

    expect(api.search).toHaveBeenCalledWith('hello', '', 1, '')
    expect(wrapper!.text()).toContain('1')
    expect(wrapper!.text()).toContain('match for')
    expect(wrapper!.text()).toContain('Sample Show')
    expect(wrapper!.text()).toContain('hello world')
    expect(wrapper!.find('.scene-card').exists()).toBe(true)
  })

  it('switches retrieval mode through the route and refetches', async () => {
    vi.mocked(api.search).mockResolvedValue(response({ mode: 'semantic' }))
    const router = await mountView('/search?q=hello')

    const semantic = wrapper!
      .findAll('.mode-btn')
      .find((button) => button.text() === 'Semantic')!
    await semantic.trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query).toMatchObject({ q: 'hello', mode: 'semantic' })
    expect(api.search).toHaveBeenLastCalledWith('hello', '', 1, 'semantic')
  })

  it('surfaces search failures without dropping the results area', async () => {
    vi.mocked(api.search).mockRejectedValue(new Error('search down'))

    await mountView('/search?q=hello')

    expect(wrapper!.find('.error-banner').text()).toBe('search down')
  })
})

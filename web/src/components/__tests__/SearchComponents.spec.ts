import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createVuetify } from 'vuetify'
import { VPagination } from 'vuetify/components'
import type { Hit, Quote, SearchResponse } from '../../api'
import HitCard from '../search/HitCard.vue'
import QuoteGrid from '../search/QuoteGrid.vue'
import SearchMatches from '../search/SearchMatches.vue'
import SearchResults from '../search/SearchResults.vue'

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
    contextBefore: [{ idx: 1, start: 60, end: 64, text: 'a preceding line' }],
    contextAfter: [{ idx: 2, start: 73, end: 75, text: 'a following line' }],
    thumbUrl: '/api/thumb/7',
    score: 0.75,
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
    semanticAvailable: false,
    ...extra,
  }
}

describe('QuoteGrid', () => {
  it('renders quote cards and reports the selected quote', async () => {
    const selected = quote()
    const wrapper = mount(QuoteGrid, { props: { quotes: [selected] } })

    expect(wrapper.text()).toContain('“A memorable line”')
    expect(wrapper.text()).toContain('Sample Show · S01E02')
    expect(wrapper.text()).toContain('1:05')

    await wrapper.find('.quote-card').trigger('click')
    expect(wrapper.emitted('open-quote')?.[0]).toEqual([selected])
  })
})

describe('HitCard', () => {
  it('renders metadata, context and the relevance score', () => {
    const wrapper = mount(HitCard, {
      props: { hit: hit(), activeMode: 'semantic' },
    })

    expect(wrapper.text()).toContain('Sample Show')
    expect(wrapper.text()).toContain('S01E02')
    expect(wrapper.text()).toContain('hello world')
    expect(wrapper.text()).toContain('a preceding line')
    expect(wrapper.text()).toContain('a following line')
    expect(wrapper.text()).toContain('1:05')
    expect(wrapper.text()).toContain('75%')
    expect(wrapper.find('img').attributes('src')).toBe('/api/thumb/7')
    expect(wrapper.find('.icon-btn').attributes('href')).toContain('download=1')
  })

  it('opens the clip from both the card and the action button', async () => {
    const sample = hit()
    const wrapper = mount(HitCard, { props: { hit: sample, activeMode: 'keyword' } })

    await wrapper.find('.scene-card').trigger('click')
    await wrapper.find('.btn-text').trigger('click')

    expect(wrapper.emitted('open-clip')).toHaveLength(2)
    expect(wrapper.emitted('open-clip')?.[0]).toEqual([sample])
  })

  it('hides the score for keyword searches', () => {
    const wrapper = mount(HitCard, { props: { hit: hit(), activeMode: 'keyword' } })
    expect(wrapper.find('.score').exists()).toBe(false)
  })
})

describe('SearchMatches', () => {
  it('renders title match counts', () => {
    const wrapper = mount(SearchMatches, {
      props: {
        matches: [
          { mediaId: 1, label: 'First Show', count: 3 },
          { mediaId: 2, label: 'Second Show', count: 1 },
        ],
      },
    })

    const rows = wrapper.findAll('.match-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('First Show')
    expect(rows[0].text()).toContain('3 lines')
    expect(rows[1].text()).toContain('1 line')
  })
})

describe('SearchResults', () => {
  const vuetify = createVuetify({ components: { VPagination } })

  it('composes the results header, hit grid, title summary and pager', async () => {
    const wrapper = mount(SearchResults, {
      props: {
        query: 'hello',
        results: response({ totalHits: 2, totalPages: 2, hits: [hit(), hit({ chunkId: '7_1', mediaId: 7 })] }),
        loading: false,
        activeMode: 'keyword',
        page: 1,
      },
      global: { plugins: [vuetify] },
    })

    expect(wrapper.text()).toContain('2')
    expect(wrapper.text()).toContain('matches for')
    expect(wrapper.text()).toContain('in 1 title')
    expect(wrapper.findAll('.scene-card')).toHaveLength(2)
    expect(wrapper.text()).toContain('Matches by title')
    expect(wrapper.text()).toContain('2 lines')
    expect(wrapper.find('.pager').exists()).toBe(true)

    await wrapper.findAll('.scene-card')[0].trigger('click')
    expect(wrapper.emitted('open-clip')).toHaveLength(1)
  })

  it('reports mode changes only when semantic search is available', async () => {
    const wrapper = mount(SearchResults, {
      props: {
        query: 'hello',
        results: response({ semanticAvailable: true }),
        loading: false,
        activeMode: 'keyword',
        page: 1,
      },
      global: { plugins: [vuetify] },
    })

    const semantic = wrapper.findAll('.mode-btn').find((button) => button.text() === 'Semantic')!
    await semantic.trigger('click')
    expect(wrapper.emitted('set-mode')?.[0]).toEqual(['semantic'])
  })

  it('renders a skeleton instead of stale results while loading', () => {
    const wrapper = mount(SearchResults, {
      props: {
        query: 'hello',
        results: response(),
        loading: true,
        activeMode: 'keyword',
        page: 1,
      },
      global: { plugins: [vuetify] },
    })

    expect(wrapper.findAll('.skeleton')).toHaveLength(6)
    expect(wrapper.find('.matches').exists()).toBe(false)
    expect(wrapper.find('.pager').exists()).toBe(false)
  })
})

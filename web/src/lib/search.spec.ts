import { describe, expect, it } from 'vitest'
import type { Hit, SearchResponse } from '../api'
import {
  activeSearchMode,
  contextPreview,
  durationLabel,
  heroCount,
  matchPct,
  pad,
  quoteTitle,
  searchModeFromQuery,
  searchModeHint,
  summarizeMatches,
  timecode,
} from './search'

function hit(extra: Partial<Hit> = {}): Hit {
  return {
    chunkId: '1',
    text: 'hello',
    textMarked: 'hello',
    mediaId: 1,
    mediaTitle: 'Movie',
    showTitle: '',
    season: 0,
    episode: 0,
    year: 2020,
    startSec: 10,
    endSec: 20,
    cues: [],
    contextBefore: [],
    contextAfter: [],
    thumbUrl: '/thumb',
    ...extra,
  }
}

function response(extra: Partial<SearchResponse> = {}): SearchResponse {
  return {
    query: 'hello',
    page: 1,
    perPage: 20,
    totalHits: 0,
    totalPages: 0,
    hits: [],
    titleMatches: [],
    mode: 'keyword',
    semanticAvailable: false,
    ...extra,
  }
}

describe('search modes', () => {
  it('accepts only the shareable mode values', () => {
    expect(searchModeFromQuery('semantic')).toBe('semantic')
    expect(searchModeFromQuery('hybrid')).toBe('hybrid')
    expect(searchModeFromQuery('keyword')).toBe('keyword')
    expect(searchModeFromQuery('all')).toBe('')
    expect(searchModeFromQuery(['semantic'])).toBe('')
    expect(searchModeFromQuery(undefined)).toBe('')
  })

  it('prefers the selected mode over the executed one', () => {
    expect(activeSearchMode('semantic', response({ mode: 'keyword' }))).toBe('semantic')
  })

  it('reflects the executed mode when no mode was selected', () => {
    expect(activeSearchMode('', response({ mode: 'semantic' }))).toBe('semantic')
    expect(activeSearchMode('', response({ mode: 'hybrid' }))).toBe('hybrid')
    expect(activeSearchMode('', response({ mode: 'keyword' }))).toBe('keyword')
    expect(activeSearchMode('', null)).toBe('keyword')
  })

  it('explains each retrieval strategy', () => {
    expect(searchModeHint('semantic')).toContain('similar meaning')
    expect(searchModeHint('hybrid')).toContain('Blends')
    expect(searchModeHint('keyword')).toContain('All words must match')
  })
})

describe('time formatting', () => {
  it('pads small numbers', () => {
    expect(pad(1)).toBe('01')
    expect(pad(12)).toBe('12')
  })

  it('formats timecodes with hours only when needed', () => {
    expect(timecode(0)).toBe('0:00')
    expect(timecode(65)).toBe('1:05')
    expect(timecode(3665)).toBe('1:01:05')
  })

  it('formats clip durations as seconds under a minute', () => {
    expect(durationLabel(10, 19.5)).toBe('9.5s')
    expect(durationLabel(10, 20)).toBe('10s')
    expect(durationLabel(0, 65)).toBe('1:05')
    expect(durationLabel(20, 10)).toBe('0s')
  })
})

describe('quote labels', () => {
  it('uses the show and episode when present', () => {
    expect(
      quoteTitle({
        chunkId: '1',
        text: 'line',
        mediaId: 1,
        mediaTitle: 'Movie',
        showTitle: 'Show',
        season: 1,
        episode: 2,
        year: 2020,
        startSec: 0,
        endSec: 1,
        thumbUrl: '',
      }),
    ).toBe('Show · S01E02')
  })

  it('falls back to the media title and year', () => {
    expect(
      quoteTitle({
        chunkId: '1',
        text: 'line',
        mediaId: 1,
        mediaTitle: 'Movie',
        showTitle: '',
        season: 0,
        episode: 0,
        year: 2020,
        startSec: 0,
        endSec: 1,
        thumbUrl: '',
      }),
    ).toBe('Movie · 2020')
  })
})

describe('result summaries', () => {
  it('clamps relevance scores to whole percentages', () => {
    expect(matchPct(0.523)).toBe('52%')
    expect(matchPct(1.4)).toBe('100%')
    expect(matchPct(-0.2)).toBe('0%')
  })

  it('keeps the first two context lines with stable keys', () => {
    expect(
      contextPreview(
        hit({
          contextBefore: [
            { idx: 1, start: 0, end: 1, text: 'before one' },
            { idx: 2, start: 0, end: 1, text: 'before two' },
          ],
          contextAfter: [{ idx: 3, start: 0, end: 1, text: 'after' }],
        }),
      ),
    ).toEqual([
      { key: 'b1', text: 'before one' },
      { key: 'b2', text: 'before two' },
    ])
  })

  it('counts distinct titles represented by the page', () => {
    expect(heroCount(undefined)).toBe(0)
    expect(heroCount([hit({ mediaId: 1 }), hit({ mediaId: 1 }), hit({ mediaId: 2 })])).toBe(2)
  })

  it('groups hits by title, busiest first, with a limit', () => {
    const matches = summarizeMatches(
      [
        hit({ mediaId: 1, showTitle: 'Show A' }),
        hit({ mediaId: 2, showTitle: 'Show B' }),
        hit({ mediaId: 2, showTitle: 'Show B' }),
        hit({ mediaId: 3, mediaTitle: 'Movie C' }),
        hit({ mediaId: 4, showTitle: 'Show D' }),
      ],
      2,
    )
    expect(matches).toEqual([
      { mediaId: 2, label: 'Show B', count: 2 },
      { mediaId: 1, label: 'Show A', count: 1 },
    ])
  })
})

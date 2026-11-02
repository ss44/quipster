// Pure helpers for the search view. The view and its sub-components share
// these so formatting, mode resolution and match grouping stay consistent
// without duplicating logic in templates.

import type { Hit, Quote, SearchMode, SearchResponse } from '../api'

/** A compact "results by title" row. */
export interface TitleMatchSummary {
  mediaId: number
  label: string
  count: number
}

/** Selectable retrieval modes, in the order shown in the toggle. */
export const SEARCH_MODE_OPTIONS: { value: Exclude<SearchMode, ''>; label: string }[] = [
  { value: 'keyword', label: 'Keyword' },
  { value: 'hybrid', label: 'Hybrid' },
  { value: 'semantic', label: 'Semantic' },
]

/**
 * Reads the shareable ?mode= query parameter. Unknown or repeated values fall
 * back to an empty mode so the server can choose its default.
 */
export function searchModeFromQuery(value: unknown): SearchMode {
  return value === 'keyword' || value === 'hybrid' || value === 'semantic' ? value : ''
}

/**
 * Resolves the mode shown in the toggle. An explicit user choice wins;
 * otherwise the mode the server actually executed is reflected, defaulting to
 * keyword when no response is available yet.
 */
export function activeSearchMode(
  selected: SearchMode,
  response: SearchResponse | null,
): Exclude<SearchMode, ''> {
  if (selected) return selected
  const executed = response?.mode
  return executed === 'semantic' || executed === 'hybrid' ? executed : 'keyword'
}

/** One-line explanation of the active retrieval strategy. */
export function searchModeHint(mode: Exclude<SearchMode, ''>): string {
  switch (mode) {
    case 'semantic':
      return 'Matches dialogue with similar meaning'
    case 'hybrid':
      return 'Blends exact words and similar meaning'
    default:
      return 'All words must match · “quoted” for exact phrase'
  }
}

/** Zero-pads a season or episode number. */
export function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** Formats seconds as m:ss, or h:mm:ss for content over an hour. */
export function timecode(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  return h > 0
    ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
    : `${m}:${String(s).padStart(2, '0')}`
}

/** Clip duration badge: seconds below a minute, timecode above it. */
export function durationLabel(start: number, end: number): string {
  const secs = Math.max(0, end - start)
  return secs >= 60 ? timecode(secs) : `${secs.toFixed(1).replace(/\.0$/, '')}s`
}

/** Title and episode/year line for a landing quote card. */
export function quoteTitle(quote: Quote): string {
  const parts = [quote.showTitle || quote.mediaTitle]
  if (quote.showTitle && quote.season) parts.push(`S${pad(quote.season)}E${pad(quote.episode)}`)
  else if (quote.year) parts.push(String(quote.year))
  return parts.join(' · ')
}

/** Relevance score as a clamped whole percentage. */
export function matchPct(score: number): string {
  return `${Math.round(Math.min(Math.max(score, 0), 1) * 100)}%`
}

/** One or two surrounding subtitle lines shown under a hit. */
export function contextPreview(hit: Hit): { key: string; text: string }[] {
  const out: { key: string; text: string }[] = []
  for (const cue of hit.contextBefore ?? []) out.push({ key: `b${cue.idx}`, text: cue.text })
  for (const cue of hit.contextAfter ?? []) out.push({ key: `a${cue.idx}`, text: cue.text })
  return out.slice(0, 2)
}

/** Number of distinct titles represented by the current page of hits. */
export function heroCount(hits: Hit[] | undefined): number {
  return new Set((hits ?? []).map((hit) => hit.mediaId)).size
}

/**
 * Groups the current page of hits by title and returns the busiest titles.
 * This is a lightweight summary of the page, not a second search.
 */
export function summarizeMatches(hits: Hit[] | undefined, limit = 12): TitleMatchSummary[] {
  const map = new Map<number, TitleMatchSummary>()
  for (const hit of hits ?? []) {
    const entry = map.get(hit.mediaId)
    if (entry) {
      entry.count++
    } else {
      map.set(hit.mediaId, {
        mediaId: hit.mediaId,
        label: hit.showTitle || hit.mediaTitle,
        count: 1,
      })
    }
  }
  return [...map.values()]
    .sort((a, b) => b.count - a.count)
    .slice(0, limit)
}

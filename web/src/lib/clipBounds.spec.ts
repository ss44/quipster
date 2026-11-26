import { describe, expect, it } from 'vitest'
import {
  formatTimestamp,
  overlappingCues,
  parseTimestamp,
  setClipEnd,
  setClipStart,
  transcriptClipBounds,
} from './clipBounds'

describe('clipBounds helpers', () => {
  it('computes padded transcript bounds and clamps to video edges', () => {
    expect(transcriptClipBounds(10, 15, 250, 600)).toEqual({ start: 9.75, end: 15.25 })
    expect(transcriptClipBounds(0.1, 5, 250, 600)).toEqual({ start: 0, end: 5.25 })
    expect(transcriptClipBounds(598, 599.9, 250, 600)).toEqual({ start: 597.75, end: 600 })
    expect(transcriptClipBounds(10, 15, 250, 0)).toEqual({ start: 9.75, end: 15.25 })
  })

  it('clamps start adjustments against zero, end, and maxClipSec while allowing shrinking', () => {
    const limits = { durationSec: 600, maxClipSec: 120 }
    expect(setClipStart({ start: 10, end: 20 }, 8.5, limits)).toEqual({ start: 8.5, end: 20 })
    expect(setClipStart({ start: 10, end: 20 }, -5, limits)).toEqual({ start: 0, end: 20 })
    expect(setClipStart({ start: 10, end: 20 }, 25, limits)).toEqual({ start: 19.9, end: 20 })

    // Expanding earlier cannot exceed maxClipSec.
    expect(setClipStart({ start: 150, end: 260 }, 100, limits)).toEqual({ start: 140, end: 260 })
    // Shrinking an already oversized clip is still allowed.
    expect(setClipStart({ start: 50, end: 200 }, 70, limits)).toEqual({ start: 70, end: 200 })
  })

  it('clamps end adjustments against start, durationSec, and maxClipSec while allowing shrinking', () => {
    const limits = { durationSec: 100, maxClipSec: 30 }
    expect(setClipEnd({ start: 10, end: 20 }, 22.5, limits)).toEqual({ start: 10, end: 22.5 })
    expect(setClipEnd({ start: 10, end: 20 }, 5, limits)).toEqual({ start: 10, end: 10.1 })
    expect(setClipEnd({ start: 85, end: 95 }, 110, limits)).toEqual({ start: 85, end: 100 })
    expect(setClipEnd({ start: 10, end: 20 }, 60, limits)).toEqual({ start: 10, end: 40 })
    // Shrinking an already oversized clip is still allowed.
    expect(setClipEnd({ start: 10, end: 60 }, 50, limits)).toEqual({ start: 10, end: 50 })
  })

  it('formats timestamps with centisecond precision', () => {
    expect(formatTimestamp(0)).toBe('0:00.00')
    expect(formatTimestamp(9.75)).toBe('0:09.75')
    expect(formatTimestamp(65.5)).toBe('1:05.50')
    expect(formatTimestamp(3661.25)).toBe('1:01:01.25')
    expect(formatTimestamp(-3)).toBe('0:00.00')
  })

  it('parses seconds, M:SS, and H:MM:SS timestamps and rejects invalid input', () => {
    expect(parseTimestamp('12')).toBe(12)
    expect(parseTimestamp('9.75')).toBe(9.75)
    expect(parseTimestamp('0:09.75')).toBe(9.75)
    expect(parseTimestamp('1:05.5')).toBe(65.5)
    expect(parseTimestamp('1:01:01.25')).toBe(3661.25)
    expect(parseTimestamp(' 2:30 ')).toBe(150)

    expect(parseTimestamp('')).toBeNull()
    expect(parseTimestamp('-5')).toBeNull()
    expect(parseTimestamp('1:75')).toBeNull()
    expect(parseTimestamp('1:65:10')).toBeNull()
    expect(parseTimestamp('1:2:3:4')).toBeNull()
    expect(parseTimestamp('abc')).toBeNull()
  })

  it('selects cues overlapping the custom clip window without boundary bleed', () => {
    const cues = [
      { idx: 0, start: 5, end: 10, text: 'before' },
      { idx: 1, start: 10, end: 15, text: 'first' },
      { idx: 2, start: 15, end: 20, text: 'second' },
      { idx: 3, start: 25, end: 30, text: 'after' },
    ]
    expect(overlappingCues(cues, 10, 15).map((c) => c.idx)).toEqual([1])
    expect(overlappingCues(cues, 9.5, 16).map((c) => c.idx)).toEqual([0, 1, 2])
    expect(overlappingCues(cues, 20.5, 24.5).map((c) => c.idx)).toEqual([])
  })
})

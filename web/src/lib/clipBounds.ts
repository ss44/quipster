import type { CueDTO } from '../api'

export interface ClipBounds {
  start: number
  end: number
}

export interface ClipBoundsLimits {
  /** Total media duration in seconds (0 if unknown). */
  durationSec: number
  /** Maximum permitted clip span in seconds (0 means unlimited). */
  maxClipSec: number
}

/** Smallest allowed clip duration in seconds (matches 0.1s fine step). */
export const MIN_CLIP_SEC = 0.1

/** Matches store.CuesBetween boundary epsilon so live preview mirrors FFmpeg burn-in. */
const CUE_OVERLAP_EPS = 0.005

/** Rounds seconds to millisecond precision to avoid IEEE-754 stepping drift. */
export function roundMs(sec: number): number {
  return Math.round(sec * 1000) / 1000
}

/**
 * Computes the effective [start, end] clip window in media seconds for a
 * transcript selection plus symmetric padding.
 */
export function transcriptClipBounds(
  selStart: number,
  selEnd: number,
  padMs: number,
  durationSec: number,
): ClipBounds {
  const padSec = Math.max(0, padMs) / 1000
  const start = Math.max(0, roundMs(selStart - padSec))
  let end = roundMs(selEnd + padSec)
  if (durationSec > 0) {
    end = Math.min(roundMs(durationSec), end)
  }
  if (end <= start) {
    end = roundMs(start + MIN_CLIP_SEC)
    if (durationSec > 0 && end > durationSec) {
      return { start: Math.max(0, roundMs(durationSec - MIN_CLIP_SEC)), end: roundMs(durationSec) }
    }
  }
  return { start, end }
}

/**
 * Updates the clip start time while keeping it within [0, current.end - MIN_CLIP_SEC]
 * and never expanding past limits.maxClipSec (shrinking an already-oversized clip
 * is always permitted).
 */
export function setClipStart(
  current: ClipBounds,
  targetStart: number,
  limits: ClipBoundsLimits,
): ClipBounds {
  if (!Number.isFinite(targetStart)) return current
  const end = current.end
  const maxStart = Math.max(0, roundMs(end - MIN_CLIP_SEC))
  let minStart = 0
  if (limits.maxClipSec > 0) {
    const allowedSpan = Math.max(limits.maxClipSec, end - current.start)
    minStart = Math.max(0, roundMs(end - allowedSpan))
  }
  const start = roundMs(Math.min(maxStart, Math.max(minStart, targetStart)))
  return { start, end }
}

/**
 * Updates the clip end time while keeping it within [current.start + MIN_CLIP_SEC, durationSec]
 * and never expanding past limits.maxClipSec (shrinking an already-oversized clip
 * is always permitted).
 */
export function setClipEnd(
  current: ClipBounds,
  targetEnd: number,
  limits: ClipBoundsLimits,
): ClipBounds {
  if (!Number.isFinite(targetEnd)) return current
  const start = current.start
  const minEnd = roundMs(start + MIN_CLIP_SEC)
  let maxEnd = limits.durationSec > 0 ? roundMs(limits.durationSec) : Number.POSITIVE_INFINITY
  if (limits.maxClipSec > 0) {
    const allowedSpan = Math.max(limits.maxClipSec, current.end - start)
    maxEnd = Math.min(maxEnd, roundMs(start + allowedSpan))
  }
  if (maxEnd < minEnd) {
    return current
  }
  const end = roundMs(Math.max(minEnd, Math.min(maxEnd, targetEnd)))
  return { start, end }
}

/**
 * Formats media seconds as M:SS.cc or H:MM:SS.cc (centiseconds) so sub-second
 * trims and default 250ms padding display accurately without rounding drift.
 */
export function formatTimestamp(seconds: number): string {
  const safe = Number.isFinite(seconds) && seconds > 0 ? seconds : 0
  const totalCs = Math.round(safe * 100)
  const cs = totalCs % 100
  const totalSec = Math.floor(totalCs / 100)
  const s = totalSec % 60
  const totalMin = Math.floor(totalSec / 60)
  const m = totalMin % 60
  const h = Math.floor(totalMin / 60)
  const pad2 = (n: number) => String(n).padStart(2, '0')
  const base = h > 0 ? `${h}:${pad2(m)}:${pad2(s)}` : `${m}:${pad2(s)}`
  return `${base}.${pad2(cs)}`
}

/**
 * Parses a user-entered timestamp into non-negative seconds. Accepts:
 * - plain seconds: "12", "12.5", "9.75"
 * - minutes:seconds: "1:05", "0:09.75"
 * - hours:minutes:seconds: "1:02:03.5"
 * Returns null for malformed, empty, or out-of-range inputs.
 */
export function parseTimestamp(raw: string): number | null {
  const trimmed = raw.trim()
  if (!trimmed) return null
  const parts = trimmed.split(':')
  if (parts.length > 3) return null
  const secPart = parts[parts.length - 1]
  if (!/^\d+(?:\.\d+)?$/.test(secPart)) return null
  const sec = Number(secPart)
  if (!Number.isFinite(sec)) return null

  if (parts.length === 1) {
    return roundMs(sec)
  }
  if (sec >= 60) return null

  const minPart = parts[parts.length - 2]
  if (!/^\d+$/.test(minPart)) return null
  const min = Number(minPart)

  if (parts.length === 2) {
    return roundMs(min * 60 + sec)
  }
  if (min >= 60) return null

  const hourPart = parts[0]
  if (!/^\d+$/.test(hourPart)) return null
  const hours = Number(hourPart)
  return roundMs(hours * 3600 + min * 60 + sec)
}

/**
 * Filters loaded subtitle cues to those overlapping [startSec, endSec],
 * mirroring SQLite's CuesBetween epsilon so the browser overlay matches
 * burned-in exports when bounds are adjusted independently of transcript lines.
 */
export function overlappingCues(cues: readonly CueDTO[], startSec: number, endSec: number): CueDTO[] {
  let lo = startSec
  let hi = endSec
  if (hi - lo > 4 * CUE_OVERLAP_EPS) {
    lo += CUE_OVERLAP_EPS
    hi -= CUE_OVERLAP_EPS
  }
  return cues.filter((c) => c.start < hi && c.end > lo)
}

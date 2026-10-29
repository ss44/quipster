// The stat-card progress footer contract, shared by the admin overview cards
// so every card renders the same unit line + bar shape on the same baseline.

/** Progress footer of a stat card; rendered bottom-pinned by StatProgress. */
export interface StatProgressModel {
  /** Unit line, e.g. "12 of 40 files processed" — names the countable unit. */
  label: string
  /** Fill fraction 0-100; ignored while `indeterminate` or `empty` is set. */
  pct: number
  /**
   * Sliding strip for work whose total is not yet knowable: the index is
   * keeping up with processing, so a full bar would read as "done" wrongly.
   */
  indeterminate?: boolean
  /**
   * Renders the bare track with a "—" readout: nothing is countable yet
   * (0 of 0), which must not be presented as completion.
   */
  empty?: boolean
  /** Right-hand readout override; defaults to the percentage or "—". */
  readout?: string
}

/**
 * Clamped integer percentage of `part` within `total`. An unknown total is
 * not a percentage: it yields 0 and callers decide how to present that.
 */
export function percent(part: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.round((part / total) * 100))
}

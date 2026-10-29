// Shared display helpers for job rows, used by the admin overview and the
// recent-jobs panel so both render identical status and timing text.

import type { Job } from '../api'

/** Status dot tone class for a job status ('' when merely queued). */
export function statusClass(status: string): string {
  switch (status) {
    case 'done':
      return 'ok'
    case 'error':
      return 'err'
    case 'running':
      return 'run'
    default:
      return ''
  }
}

/** Base filename of a media path; empty input yields an empty string. */
export function fileBase(path: string): string {
  if (!path) return ''
  const parts = path.split('/')
  return parts[parts.length - 1]
}

/** Live elapsed time of a job, or '' when it never started. */
export function jobElapsed(job: Job, now: number): string {
  if (!job.startedAt) return ''
  const end = job.finishedAt || now
  const ms = Math.max(0, end - job.startedAt)
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${Math.round(ms / 1000)}s`
  return `${(ms / 60000).toFixed(1)}m`
}

/** Wait time of a queued job, counting from enqueue to claim. */
export function jobWait(job: Job, now: number): string {
  const start = job.startedAt || now
  const ms = Math.max(0, start - job.createdAt)
  if (ms < 60000) return `${Math.round(ms / 1000)}s`
  return `${Math.round(ms / 60000)}m`
}

/** Duration column for the recent-jobs table: wait time, run time or a dash. */
export function jobDuration(job: Job, now: number): string {
  if (job.status === 'queued') return `wait: ${jobWait(job, now)}`
  if (!job.startedAt) return '—'
  if (!job.finishedAt && job.status !== 'running') return '—'
  return jobElapsed(job, now)
}

/** Locale clock time for a job timestamp; 0 renders as a dash. */
export function jobClockTime(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

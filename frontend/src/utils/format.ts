import type { DeliveryDetail } from '../types/api'

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
}

export function relativeTime(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return '—'
  const seconds = Math.round((new Date(iso).getTime() - now) / 1000)
  const abs = Math.abs(seconds)
  const unit =
    abs < 60
      ? [seconds, 'second']
      : abs < 3600
        ? [Math.round(seconds / 60), 'minute']
        : abs < 86400
          ? [Math.round(seconds / 3600), 'hour']
          : [Math.round(seconds / 86400), 'day']
  return new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' }).format(
    unit[0] as number,
    unit[1] as Intl.RelativeTimeFormatUnit,
  )
}

export function formatDuration(ms: number): string {
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(2)} s`
}

export function shortId(id: string): string {
  return id.slice(0, 8)
}

export function formatPercent(rate: number | null): string {
  return rate === null ? '—' : `${(rate * 100).toFixed(1)}%`
}

/** Plain-language reasons for each failure category the backend records. */
const CATEGORY_REASON: Record<string, string> = {
  network: 'could not be reached (connection or DNS failure)',
  timeout: 'did not respond in time',
  http_retryable: 'answered that it was temporarily unavailable (408/425)',
  http_rate_limited: 'asked EventMesh to slow down (429)',
  http_server_error: 'returned a server error (5xx)',
  http_redirect: 'answered with a redirect, which EventMesh does not follow',
  http_client_error: 'rejected the request (4xx)',
  blocked_destination: 'is not an allowed destination (private or reserved address)',
}

export function categoryLabel(category: string | null): string {
  if (!category) return '—'
  return category.replace(/_/g, ' ')
}

/**
 * Explains in one sentence why a delivery ended where it did. The wording is
 * derived only from fields the API returns, never from a model.
 */
export function explainDelivery(d: DeliveryDetail): string {
  const attempts = d.attempt_count
  const plural = attempts === 1 ? 'attempt' : 'attempts'
  switch (d.status) {
    case 'delivered':
      return attempts <= 1
        ? 'The endpoint accepted the delivery on the first attempt.'
        : `The endpoint accepted the delivery after ${attempts} ${plural}.`
    case 'pending':
      return d.replay_count > 0 || attempts > 0
        ? 'The delivery is queued again and waiting for a worker.'
        : 'The delivery is queued and waiting for a worker.'
    case 'processing':
      return 'A worker is sending this delivery right now.'
    case 'cancelled':
      return d.endpoint_deleted
        ? 'The endpoint was deleted before this delivery was sent, so it was cancelled.'
        : 'The endpoint was disabled before this delivery was sent, so it was cancelled.'
    case 'retry_scheduled':
      return `The last attempt failed (${categoryLabel(d.last_error_category)}); a retry is scheduled for ${formatDateTime(d.next_attempt_at)}.`
    case 'dead_letter': {
      const category = d.last_error_category ?? ''
      const reason = CATEGORY_REASON[category] ?? 'failed'
      const status = d.last_response_status
      const statusPart = status ? ` and returned ${status}` : ''
      if (category === 'http_server_error' && status) {
        return `The endpoint returned ${status} on all ${attempts} ${plural}, so the delivery was moved to dead letter.`
      }
      const retried = attempts > 1 ? `after ${attempts} attempts` : 'on the first attempt'
      return `The endpoint ${reason}${category === 'http_server_error' ? '' : statusPart} ${retried}, so the delivery was moved to dead letter.`
    }
  }
}

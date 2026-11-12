import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { ApiError, api } from '../api/client'
import { PageHeader } from '../components/Layout'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  Select,
  Spinner,
  StatusBadge,
  TD,
  TH,
  TableWrap,
} from '../components/ui'
import { useCursorList } from '../hooks/useCursorList'
import { DELIVERY_STATUSES } from '../types/api'
import {
  categoryLabel,
  explainDelivery,
  formatDateTime,
  formatDuration,
  relativeTime,
  shortId,
} from '../utils/format'
import { dayBoundary, useUrlFilters } from './Events'

export function DeliveriesPage() {
  const { filters, set, clear } = useUrlFilters(['status', 'endpoint_id', 'event_type', 'from', 'to'])
  const endpoints = useQuery({ queryKey: ['endpoints'], queryFn: api.listEndpoints })
  const request = {
    ...filters,
    from: dayBoundary(filters.from, false) ?? '',
    to: dayBoundary(filters.to, true) ?? '',
  }
  const list = useCursorList('deliveries', request, api.listDeliveries)

  return (
    <>
      <PageHeader title="Deliveries" />
      <Card>
        <form
          className="grid gap-3 md:grid-cols-5"
          onSubmit={(e) => e.preventDefault()}
          aria-label="Delivery filters"
        >
          <Select
            aria-label="Status"
            value={filters.status ?? ''}
            onChange={(e) => set('status', e.target.value)}
          >
            <option value="">Any status</option>
            {DELIVERY_STATUSES.map((s) => (
              <option key={s} value={s}>
                {s.replace('_', ' ')}
              </option>
            ))}
          </Select>
          <Select
            aria-label="Endpoint"
            value={filters.endpoint_id ?? ''}
            onChange={(e) => set('endpoint_id', e.target.value)}
          >
            <option value="">Any endpoint</option>
            {endpoints.data?.items.map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </Select>
          <Input
            aria-label="Event type"
            placeholder="Event type"
            value={filters.event_type ?? ''}
            onChange={(e) => set('event_type', e.target.value.trim())}
          />
          <Input
            aria-label="From date"
            type="date"
            value={filters.from ?? ''}
            onChange={(e) => set('from', e.target.value)}
          />
          <Input
            aria-label="To date"
            type="date"
            value={filters.to ?? ''}
            onChange={(e) => set('to', e.target.value)}
          />
        </form>
        {Object.keys(filters).length > 0 && (
          <div className="pt-3">
            <Button variant="ghost" onClick={clear}>
              Clear filters
            </Button>
          </div>
        )}
      </Card>
      <Card>
        {list.isPending && <Spinner />}
        {list.isError && <ErrorState error={list.error} onRetry={() => void list.refetch()} />}
        {list.isSuccess && list.rows.length === 0 && <EmptyState title="No deliveries match" />}
        {list.rows.length > 0 && (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>Delivery</th>
                  <th className={TH}>Event type</th>
                  <th className={TH}>Endpoint</th>
                  <th className={TH}>Status</th>
                  <th className={TH}>Attempts</th>
                  <th className={TH}>Last attempt</th>
                  <th className={TH}>Response</th>
                </tr>
              </thead>
              <tbody>
                {list.rows.map((d) => (
                  <tr key={d.id} className="border-t border-border">
                    <td className={TD}>
                      <Link className="font-mono text-accent hover:underline" to={`/deliveries/${d.id}`}>
                        {shortId(d.id)}
                      </Link>
                    </td>
                    <td className={TD}>{d.event_type}</td>
                    <td className={TD}>{d.endpoint_name}</td>
                    <td className={TD}>
                      <StatusBadge status={d.status} />
                    </td>
                    <td className={TD}>{d.attempt_count}</td>
                    <td className={TD}>{formatDateTime(d.last_attempt_at)}</td>
                    <td className={TD}>{d.last_response_status ?? categoryLabel(d.last_error_category)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
        {list.hasNextPage && (
          <div className="pt-3">
            <Button onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
              Load more
            </Button>
          </div>
        )}
      </Card>
    </>
  )
}

export function DeliveryDetailPage() {
  const { id = '' } = useParams()
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['delivery', id], queryFn: () => api.getDelivery(id) })
  const [replayError, setReplayError] = useState<string | null>(null)
  const replay = useMutation({
    mutationFn: () => api.replayDelivery(id),
    onSuccess: () => {
      setReplayError(null)
      void qc.invalidateQueries({ queryKey: ['delivery', id] })
      void qc.invalidateQueries({ queryKey: ['deliveries'] })
    },
    onError: (e) => setReplayError(e instanceof ApiError ? e.message : 'Could not replay the delivery.'),
  })

  if (q.isPending) return <Spinner />
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />
  const d = q.data.delivery

  return (
    <>
      <PageHeader
        title={`Delivery ${shortId(d.id)}`}
        subtitle={d.id}
        actions={
          <div className="flex items-center gap-3">
            <StatusBadge status={d.status} />
            {d.can_replay && (
              <Button variant="primary" onClick={() => replay.mutate()} disabled={replay.isPending}>
                Replay
              </Button>
            )}
          </div>
        }
      />
      {replayError && (
        <p role="alert" className="text-sm text-bad">
          {replayError}
        </p>
      )}
      <Card title="What happened">
        <p className="text-sm" data-testid="explanation">
          {explainDelivery(d)}
        </p>
        {d.status === 'dead_letter' && !d.can_replay && (
          <p className="mt-2 text-sm text-muted">
            {d.endpoint_deleted
              ? 'The endpoint was deleted, so this delivery cannot be replayed.'
              : 'Enable the endpoint to replay this delivery.'}
          </p>
        )}
      </Card>
      <div className="grid gap-4 md:grid-cols-2">
        <Card title="Details">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted">Event</dt>
            <dd>
              <Link className="text-accent hover:underline" to={`/events/${d.event_id}`}>
                {d.event_type} ({shortId(d.event_id)})
              </Link>
            </dd>
            <dt className="text-muted">Endpoint</dt>
            <dd>
              <Link className="text-accent hover:underline" to={`/endpoints/${d.endpoint_id}`}>
                {d.endpoint_name}
              </Link>
              {d.endpoint_deleted && (
                <span className="ml-2">
                  <Badge tone="neutral">Deleted</Badge>
                </span>
              )}
              {!d.endpoint_enabled && !d.endpoint_deleted && (
                <span className="ml-2">
                  <Badge tone="neutral">Disabled</Badge>
                </span>
              )}
            </dd>
            <dt className="text-muted">URL</dt>
            <dd className="break-all font-mono text-xs">{d.endpoint_url}</dd>
            <dt className="text-muted">Attempts</dt>
            <dd>
              {d.attempt_count} total · {d.cycle_attempt_count} of {d.max_attempts} in this cycle
              {d.replay_count > 0 && ` · replayed ${d.replay_count}×`}
            </dd>
            <dt className="text-muted">Next retry</dt>
            <dd>
              {d.status === 'retry_scheduled' || d.status === 'pending'
                ? `${formatDateTime(d.next_attempt_at)} (${relativeTime(d.next_attempt_at)})`
                : '—'}
            </dd>
          </dl>
        </Card>
        <Card title="Request headers sent">
          {d.request_headers.length === 0 ? (
            <EmptyState title="No request sent yet" />
          ) : (
            <dl className="space-y-1 font-mono text-xs">
              {d.request_headers.map((h) => (
                <div key={h.name} className="flex gap-2">
                  <dt className="text-muted">{h.name}:</dt>
                  <dd className="break-all">{h.value}</dd>
                </div>
              ))}
            </dl>
          )}
        </Card>
      </div>
      <Card title="Attempt timeline">
        {d.attempts.length === 0 ? (
          <EmptyState title="No attempts yet" />
        ) : (
          <ol className="space-y-3" aria-label="Attempts">
            {d.attempts.map((a) => (
              <li key={a.id} className="rounded-control border border-border p-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">Attempt {a.attempt_number}</span>
                  <Badge tone={a.trigger === 'replay' ? 'info' : 'neutral'}>{a.trigger}</Badge>
                  {a.response_status !== null && (
                    <Badge tone={a.response_status >= 200 && a.response_status < 300 ? 'ok' : 'bad'}>
                      {a.response_status}
                    </Badge>
                  )}
                  <span className="text-muted">{formatDuration(a.duration_ms)}</span>
                  <span className="text-muted">{formatDateTime(a.started_at)}</span>
                </div>
                {a.error_category && (
                  <p className="mt-1 text-bad">
                    {categoryLabel(a.error_category)}
                    {a.error_message ? `: ${a.error_message}` : ''}
                  </p>
                )}
                {a.response_body_excerpt && (
                  <pre className="mt-2 max-h-32 overflow-auto rounded-control bg-surface-2 p-2 font-mono text-xs">
                    {a.response_body_excerpt}
                  </pre>
                )}
              </li>
            ))}
          </ol>
        )}
      </Card>
    </>
  )
}

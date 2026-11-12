import { useQuery } from '@tanstack/react-query'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { JsonView } from '../components/JsonView'
import { AiSearchBar, useAiAvailability } from '../components/ai'
import { PageHeader } from '../components/Layout'
import {
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
import { EVENT_STATUSES } from '../types/api'
import { categoryLabel, formatDateTime, shortId } from '../utils/format'

/** Filters live in the URL so a view can be shared and survives a reload. */
export function useUrlFilters(keys: string[]) {
  const [params, setParams] = useSearchParams()
  const filters: Record<string, string> = {}
  for (const k of keys) {
    const v = params.get(k)
    if (v) filters[k] = v
  }
  const set = (key: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next, { replace: true })
  }
  return {
    filters,
    set,
    setAll: (next: URLSearchParams) => setParams(next, { replace: true }),
    clear: () => setParams({}, { replace: true }),
  }
}

/** An <input type="date"> value (YYYY-MM-DD) as an ISO timestamp at the start or end of that local day. */
export function dayBoundary(date: string | undefined, end: boolean): string | undefined {
  if (!date) return undefined
  const d = new Date(`${date}T${end ? '23:59:59.999' : '00:00:00'}`)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

export function EventsPage() {
  const { filters, set, setAll, clear } = useUrlFilters([
    'event_type',
    'status',
    'from',
    'to',
    'id',
    'endpoint_id',
  ])
  const ai = useAiAvailability()
  const request = {
    ...filters,
    from: dayBoundary(filters.from, false) ?? '',
    to: dayBoundary(filters.to, true) ?? '',
  }
  const list = useCursorList('events', request, api.listEvents)

  return (
    <>
      <PageHeader title="Events" />
      {ai.search && (
        <AiSearchBar
          onApply={(f) => {
            const next = new URLSearchParams()
            if (f.event_type) next.set('event_type', f.event_type)
            if (f.status) next.set('status', f.status)
            if (f.endpoint_id) next.set('endpoint_id', f.endpoint_id)
            if (f.id_search) next.set('id', f.id_search)
            if (f.from) next.set('from', f.from.slice(0, 10))
            if (f.to) next.set('to', f.to.slice(0, 10))
            setAll(next)
          }}
        />
      )}
      <Card>
        <form
          className="grid gap-3 md:grid-cols-5"
          onSubmit={(e) => e.preventDefault()}
          aria-label="Event filters"
        >
          <Input
            aria-label="Event type"
            placeholder="Event type"
            value={filters.event_type ?? ''}
            onChange={(e) => set('event_type', e.target.value.trim())}
          />
          <Select
            aria-label="Status"
            value={filters.status ?? ''}
            onChange={(e) => set('status', e.target.value)}
          >
            <option value="">Any status</option>
            {EVENT_STATUSES.map((s) => (
              <option key={s} value={s}>
                {s.replace('_', ' ')}
              </option>
            ))}
          </Select>
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
          <Input
            aria-label="Event ID"
            placeholder="Event ID"
            value={filters.id ?? ''}
            onChange={(e) => set('id', e.target.value.trim())}
          />
        </form>
        {Object.keys(filters).length > 0 && (
          <div className="pt-3">
            {filters.endpoint_id && (
              <p className="pb-2 text-sm text-muted">Filtered to endpoint {shortId(filters.endpoint_id)}.</p>
            )}
            <Button variant="ghost" onClick={clear}>
              Clear filters
            </Button>
          </div>
        )}
      </Card>
      <Card>
        {list.isPending && <Spinner />}
        {list.isError && <ErrorState error={list.error} onRetry={() => void list.refetch()} />}
        {list.isSuccess && list.rows.length === 0 && (
          <EmptyState title="No events match" hint="Change the filters, or send an event with an API key." />
        )}
        {list.rows.length > 0 && (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>Event ID</th>
                  <th className={TH}>Type</th>
                  <th className={TH}>Created</th>
                  <th className={TH}>Status</th>
                  <th className={TH}>Deliveries</th>
                </tr>
              </thead>
              <tbody>
                {list.rows.map((e) => (
                  <tr key={e.id} className="border-t border-border">
                    <td className={TD}>
                      <Link className="font-mono text-accent hover:underline" to={`/events/${e.id}`}>
                        {shortId(e.id)}
                      </Link>
                    </td>
                    <td className={TD}>{e.event_type}</td>
                    <td className={TD}>{formatDateTime(e.created_at)}</td>
                    <td className={TD}>
                      <StatusBadge status={e.status} />
                    </td>
                    <td className={TD}>{e.delivery_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
        {list.hasNextPage && (
          <div className="pt-3">
            {filters.endpoint_id && (
              <p className="pb-2 text-sm text-muted">Filtered to endpoint {shortId(filters.endpoint_id)}.</p>
            )}
            <Button onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
              Load more
            </Button>
          </div>
        )}
      </Card>
    </>
  )
}

export function EventDetailPage() {
  const { id = '' } = useParams()
  const q = useQuery({ queryKey: ['event', id], queryFn: () => api.getEvent(id) })
  if (q.isPending) return <Spinner />
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />
  const e = q.data.event
  return (
    <>
      <PageHeader
        title={e.event_type}
        subtitle={`Event ${e.id} · ${formatDateTime(e.created_at)}`}
        actions={<StatusBadge status={e.status} />}
      />
      <Card title="Payload">
        <JsonView value={e.payload} />
      </Card>
      <Card title={`Deliveries (${e.deliveries.length})`}>
        {e.deliveries.length === 0 ? (
          <EmptyState
            title="No deliveries"
            hint="No enabled endpoint subscribed to this event type when it was received."
          />
        ) : (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>Delivery</th>
                  <th className={TH}>Endpoint</th>
                  <th className={TH}>Status</th>
                  <th className={TH}>Attempts</th>
                  <th className={TH}>Error</th>
                </tr>
              </thead>
              <tbody>
                {e.deliveries.map((d) => (
                  <tr key={d.id} className="border-t border-border">
                    <td className={TD}>
                      <Link className="font-mono text-accent hover:underline" to={`/deliveries/${d.id}`}>
                        {shortId(d.id)}
                      </Link>
                    </td>
                    <td className={TD}>
                      <Link className="hover:underline" to={`/endpoints/${d.endpoint_id}`}>
                        {d.endpoint_name}
                      </Link>
                    </td>
                    <td className={TD}>
                      <StatusBadge status={d.status} />
                    </td>
                    <td className={TD}>{d.attempt_count}</td>
                    <td className={TD}>{categoryLabel(d.last_error_category)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
      </Card>
    </>
  )
}

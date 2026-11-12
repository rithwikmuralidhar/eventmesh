import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../api/client'
import { PageHeader } from '../components/Layout'
import {
  Card,
  EmptyState,
  ErrorState,
  Select,
  Spinner,
  StatusBadge,
  TD,
  TH,
  TableWrap,
} from '../components/ui'
import { useState } from 'react'
import type { StatsRange } from '../types/api'
import { categoryLabel, formatDateTime, formatPercent, shortId } from '../utils/format'

const RANGES: { value: StatsRange; label: string }[] = [
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
]

function Tile({
  label,
  value,
  tone = 'text-text',
}: {
  label: string
  value: string | number
  tone?: string
}) {
  return (
    <div className="rounded-card border border-border bg-surface p-4">
      <p className="text-xs uppercase tracking-wide text-muted">{label}</p>
      <p className={`mt-1 text-2xl font-semibold ${tone}`}>{value}</p>
    </div>
  )
}

export function OverviewPage() {
  const [range, setRange] = useState<StatsRange>('24h')
  const stats = useQuery({ queryKey: ['stats', range], queryFn: () => api.stats(range) })
  const recentEvents = useQuery({
    queryKey: ['events', 'recent'],
    queryFn: () => api.listEvents({ limit: 5 }),
  })

  return (
    <>
      <PageHeader
        title="Overview"
        actions={
          <Select
            aria-label="Time range"
            value={range}
            onChange={(e) => setRange(e.target.value as StatsRange)}
            className="w-44"
          >
            {RANGES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        }
      />
      {stats.isPending && <Spinner />}
      {stats.isError && <ErrorState error={stats.error} onRetry={() => void stats.refetch()} />}
      {stats.data && (
        <>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Tile label="Events" value={stats.data.totals.events} />
            <Tile label="Deliveries" value={stats.data.totals.deliveries} />
            <Tile label="Delivered" value={stats.data.totals.delivered} tone="text-ok" />
            <Tile label="Dead letter" value={stats.data.totals.dead_letter} tone="text-bad" />
            <Tile label="Pending" value={stats.data.totals.pending} />
            <Tile label="Success rate" value={formatPercent(stats.data.totals.success_rate)} />
          </div>
          <Card title="Delivery volume">
            {stats.data.totals.deliveries === 0 ? (
              <EmptyState
                title="No deliveries in this range"
                hint="Send an event with an API key to see activity here."
              />
            ) : (
              <div className="h-64" aria-label="Delivery volume chart">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart
                    data={stats.data.series.map((b) => ({
                      ...b,
                      label: new Date(b.start).toLocaleString(
                        undefined,
                        range === '24h' ? { hour: 'numeric' } : { month: 'short', day: 'numeric' },
                      ),
                    }))}
                  >
                    <CartesianGrid stroke="var(--c-border)" vertical={false} />
                    <XAxis dataKey="label" stroke="var(--c-muted)" fontSize={12} />
                    <YAxis allowDecimals={false} stroke="var(--c-muted)" fontSize={12} />
                    <Tooltip
                      contentStyle={{ background: 'var(--c-surface)', border: '1px solid var(--c-border)' }}
                    />
                    <Bar dataKey="delivered" name="Delivered" stackId="a" fill="var(--c-ok)" />
                    <Bar dataKey="dead_letter" name="Dead letter" stackId="a" fill="var(--c-bad)" />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </Card>
          <div className="grid gap-4 lg:grid-cols-2">
            <Card title="Recent events">
              {recentEvents.isPending && <Spinner />}
              {recentEvents.isError && <ErrorState error={recentEvents.error} />}
              {recentEvents.data?.items.length === 0 && <EmptyState title="No events yet" />}
              {recentEvents.data && recentEvents.data.items.length > 0 && (
                <TableWrap>
                  <table className="w-full">
                    <tbody>
                      {recentEvents.data.items.map((e) => (
                        <tr key={e.id} className="border-t border-border first:border-0">
                          <td className={TD}>
                            <Link className="text-accent hover:underline" to={`/events/${e.id}`}>
                              {e.event_type}
                            </Link>
                          </td>
                          <td className={TD}>
                            <StatusBadge status={e.status} />
                          </td>
                          <td className={`${TD} text-muted`}>{formatDateTime(e.created_at)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </TableWrap>
              )}
            </Card>
            <Card title="Recent failures">
              {stats.data.recent_failures.length === 0 ? (
                <EmptyState title="No failures in this range" />
              ) : (
                <TableWrap>
                  <table className="w-full">
                    <thead>
                      <tr>
                        <th className={TH}>Delivery</th>
                        <th className={TH}>Endpoint</th>
                        <th className={TH}>Reason</th>
                      </tr>
                    </thead>
                    <tbody>
                      {stats.data.recent_failures.map((f) => (
                        <tr key={f.id} className="border-t border-border">
                          <td className={TD}>
                            <Link className="text-accent hover:underline" to={`/deliveries/${f.id}`}>
                              {shortId(f.id)}
                            </Link>
                          </td>
                          <td className={TD}>{f.endpoint_name}</td>
                          <td className={TD}>
                            {categoryLabel(f.last_error_category)}
                            {f.last_response_status ? ` (${f.last_response_status})` : ''}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </TableWrap>
              )}
            </Card>
          </div>
        </>
      )}
    </>
  )
}

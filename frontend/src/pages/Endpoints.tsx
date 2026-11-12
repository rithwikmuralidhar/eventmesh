import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ApiError, api } from '../api/client'
import { PageHeader } from '../components/Layout'
import {
  Button,
  Card,
  ConfirmDialog,
  CopyButton,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Spinner,
  StatusBadge,
  TD,
  TH,
  TableWrap,
  Badge,
} from '../components/ui'
import { useCursorList } from '../hooks/useCursorList'
import type { Endpoint, EndpointInput } from '../types/api'
import { categoryLabel, formatDateTime, shortId } from '../utils/format'
import {
  endpointSchema,
  intOrNull,
  parseEventTypes,
  type EndpointForm as EndpointValues,
} from '../utils/schemas'

/** Shows a secret exactly once, with a copy button. */
export function SecretReveal({ secret, onClose }: { secret: string; onClose: () => void }) {
  return (
    <Card title="Signing secret">
      <p className="text-sm text-warn">
        Copy this secret now. It is shown only once; if you lose it, rotate the secret.
      </p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <code className="rounded-control bg-surface-2 px-2 py-1 font-mono text-sm" data-testid="secret">
          {secret}
        </code>
        <CopyButton value={secret} label="Copy secret" />
        <Button onClick={onClose}>Done</Button>
      </div>
    </Card>
  )
}

function toInput(f: EndpointValues): EndpointInput {
  return {
    name: f.name.trim(),
    url: f.url.trim(),
    event_types: parseEventTypes(f.event_types),
    enabled: f.enabled,
    rate_limit_per_second: Number(f.rate_limit_per_second),
    max_retries: intOrNull(f.max_retries),
    initial_retry_delay_ms: intOrNull(f.initial_retry_delay_ms),
    max_retry_delay_ms: intOrNull(f.max_retry_delay_ms),
  }
}

function defaults(e?: Endpoint): EndpointValues {
  return {
    name: e?.name ?? '',
    url: e?.url ?? '',
    event_types: e?.event_types.join(', ') ?? '*',
    rate_limit_per_second: String(e?.rate_limit_per_second ?? 10),
    max_retries: e?.max_retries?.toString() ?? '',
    initial_retry_delay_ms: e?.initial_retry_delay_ms?.toString() ?? '',
    max_retry_delay_ms: e?.max_retry_delay_ms?.toString() ?? '',
    enabled: e?.enabled ?? true,
  }
}

export function EndpointForm({
  endpoint,
  onSaved,
  onCancel,
}: {
  endpoint?: Endpoint
  onSaved: (secret?: string) => void
  onCancel: () => void
}) {
  const qc = useQueryClient()
  const [serverError, setServerError] = useState<string | null>(null)
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<EndpointValues>({ resolver: zodResolver(endpointSchema), defaultValues: defaults(endpoint) })

  const save = useMutation({
    mutationFn: async (f: EndpointValues) => {
      const input = toInput(f)
      if (endpoint) {
        await api.updateEndpoint(endpoint.id, input)
        return undefined
      }
      return (await api.createEndpoint(input)).secret
    },
    onSuccess: (secret) => {
      void qc.invalidateQueries({ queryKey: ['endpoints'] })
      void qc.invalidateQueries({ queryKey: ['endpoint'] })
      onSaved(secret)
    },
    onError: (e) => setServerError(e instanceof ApiError ? e.message : 'Could not save the endpoint.'),
  })

  return (
    <Card title={endpoint ? 'Edit endpoint' : 'New endpoint'}>
      <form onSubmit={handleSubmit((f) => save.mutate(f))} noValidate className="grid gap-4 md:grid-cols-2">
        <Field label="Name" htmlFor="ep-name" error={errors.name?.message}>
          <Input id="ep-name" {...register('name')} />
        </Field>
        <Field label="URL" htmlFor="ep-url" error={errors.url?.message}>
          <Input id="ep-url" placeholder="https://example.com/webhooks" {...register('url')} />
        </Field>
        <Field
          label="Event types"
          htmlFor="ep-types"
          error={errors.event_types?.message}
          hint="Comma separated: payment.completed, order.*, or *"
        >
          <Input id="ep-types" {...register('event_types')} />
        </Field>
        <Field
          label="Rate limit (per second)"
          htmlFor="ep-rate"
          error={errors.rate_limit_per_second?.message}
        >
          <Input id="ep-rate" inputMode="numeric" {...register('rate_limit_per_second')} />
        </Field>
        <Field
          label="Max retries"
          htmlFor="ep-retries"
          error={errors.max_retries?.message}
          hint="Empty uses the system default"
        >
          <Input id="ep-retries" inputMode="numeric" {...register('max_retries')} />
        </Field>
        <Field
          label="Initial retry delay (ms)"
          htmlFor="ep-initial"
          error={errors.initial_retry_delay_ms?.message}
          hint="Empty uses the system default"
        >
          <Input id="ep-initial" inputMode="numeric" {...register('initial_retry_delay_ms')} />
        </Field>
        <Field
          label="Max retry delay (ms)"
          htmlFor="ep-max"
          error={errors.max_retry_delay_ms?.message}
          hint="Empty uses the system default"
        >
          <Input id="ep-max" inputMode="numeric" {...register('max_retry_delay_ms')} />
        </Field>
        <label className="flex items-center gap-2 self-end text-sm">
          <input type="checkbox" {...register('enabled')} /> Enabled
        </label>
        {serverError && (
          <p role="alert" className="text-sm text-bad md:col-span-2">
            {serverError}
          </p>
        )}
        <div className="flex gap-2 md:col-span-2">
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {endpoint ? 'Save changes' : 'Create endpoint'}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    </Card>
  )
}

export function EndpointsPage() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['endpoints'], queryFn: api.listEndpoints })
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)
  const [toDelete, setToDelete] = useState<Endpoint | null>(null)
  const toggle = useMutation({
    mutationFn: (e: Endpoint) => api.updateEndpoint(e.id, { enabled: !e.enabled }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['endpoints'] }),
  })
  const remove = useMutation({
    mutationFn: (e: Endpoint) => api.deleteEndpoint(e.id),
    onSuccess: () => {
      setToDelete(null)
      void qc.invalidateQueries({ queryKey: ['endpoints'] })
    },
  })

  return (
    <>
      <PageHeader
        title="Endpoints"
        actions={
          !creating && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              New endpoint
            </Button>
          )
        }
      />
      {secret && <SecretReveal secret={secret} onClose={() => setSecret(null)} />}
      {creating && (
        <EndpointForm
          onCancel={() => setCreating(false)}
          onSaved={(s) => {
            setCreating(false)
            if (s) setSecret(s)
          }}
        />
      )}
      <Card>
        {list.isPending && <Spinner />}
        {list.isError && <ErrorState error={list.error} onRetry={() => void list.refetch()} />}
        {list.data?.items.length === 0 && (
          <EmptyState title="No endpoints yet" hint="Create an endpoint to start receiving events." />
        )}
        {list.data && list.data.items.length > 0 && (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>Name</th>
                  <th className={TH}>URL</th>
                  <th className={TH}>Subscriptions</th>
                  <th className={TH}>Rate</th>
                  <th className={TH}>Delivered / total</th>
                  <th className={TH}>Enabled</th>
                  <th className={TH}></th>
                </tr>
              </thead>
              <tbody>
                {list.data.items.map((e) => (
                  <tr key={e.id} className="border-t border-border">
                    <td className={TD}>
                      <Link className="text-accent hover:underline" to={`/endpoints/${e.id}`}>
                        {e.name}
                      </Link>
                    </td>
                    <td className={`${TD} max-w-56 truncate font-mono text-xs`} title={e.url}>
                      {e.url}
                    </td>
                    <td className={TD}>{e.event_types.join(', ')}</td>
                    <td className={TD}>{e.rate_limit_per_second}/s</td>
                    <td className={TD}>
                      {e.stats?.delivered ?? 0} / {e.stats?.total ?? 0}
                      {(e.stats?.dead_letter ?? 0) > 0 && (
                        <span className="ml-2">
                          <Badge tone="bad">{e.stats?.dead_letter} dead</Badge>
                        </span>
                      )}
                    </td>
                    <td className={TD}>
                      <input
                        type="checkbox"
                        role="switch"
                        aria-label={`Enable ${e.name}`}
                        checked={e.enabled}
                        disabled={toggle.isPending}
                        onChange={() => toggle.mutate(e)}
                      />
                    </td>
                    <td className={TD}>
                      <Button variant="ghost" onClick={() => setToDelete(e)}>
                        Delete
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
      </Card>
      <ConfirmDialog
        open={toDelete !== null}
        title="Delete endpoint?"
        message={`“${toDelete?.name ?? ''}” will stop receiving events and its open deliveries will be cancelled. Delivery history is kept.`}
        confirmLabel="Delete endpoint"
        busy={remove.isPending}
        onCancel={() => setToDelete(null)}
        onConfirm={() => toDelete && remove.mutate(toDelete)}
      />
    </>
  )
}

export function EndpointDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['endpoint', id], queryFn: () => api.getEndpoint(id) })
  const deliveries = useCursorList('deliveries', { endpoint_id: id }, api.listDeliveries)
  const [editing, setEditing] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)
  const [confirmRotate, setConfirmRotate] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const rotate = useMutation({
    mutationFn: () => api.rotateSecret(id),
    onSuccess: (r) => {
      setConfirmRotate(false)
      setSecret(r.secret)
    },
  })
  const remove = useMutation({
    mutationFn: () => api.deleteEndpoint(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['endpoints'] })
      navigate('/endpoints')
    },
  })

  if (q.isPending) return <Spinner />
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />
  const e = q.data.endpoint
  const retry = (v: number | null, unit = '') => (v === null ? 'system default' : `${v}${unit}`)

  return (
    <>
      <PageHeader
        title={e.name}
        subtitle={e.url}
        actions={
          <div className="flex gap-2">
            <Button onClick={() => setEditing(true)}>Edit</Button>
            <Button onClick={() => setConfirmRotate(true)}>Rotate secret</Button>
            <Button variant="danger" onClick={() => setConfirmDelete(true)}>
              Delete
            </Button>
          </div>
        }
      />
      {secret && <SecretReveal secret={secret} onClose={() => setSecret(null)} />}
      {editing && (
        <EndpointForm endpoint={e} onCancel={() => setEditing(false)} onSaved={() => setEditing(false)} />
      )}
      <div className="grid gap-4 md:grid-cols-2">
        <Card title="Configuration">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted">Status</dt>
            <dd>{e.enabled ? <Badge tone="ok">Enabled</Badge> : <Badge tone="neutral">Disabled</Badge>}</dd>
            <dt className="text-muted">Subscriptions</dt>
            <dd>{e.event_types.join(', ')}</dd>
            <dt className="text-muted">Rate limit</dt>
            <dd>{e.rate_limit_per_second} per second</dd>
            <dt className="text-muted">Max retries</dt>
            <dd>{retry(e.max_retries)}</dd>
            <dt className="text-muted">Initial delay</dt>
            <dd>{retry(e.initial_retry_delay_ms, ' ms')}</dd>
            <dt className="text-muted">Max delay</dt>
            <dd>{retry(e.max_retry_delay_ms, ' ms')}</dd>
          </dl>
        </Card>
        <Card title="Statistics">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted">Total deliveries</dt>
            <dd>{e.stats?.total ?? 0}</dd>
            <dt className="text-muted">Delivered</dt>
            <dd>{e.stats?.delivered ?? 0}</dd>
            <dt className="text-muted">Dead letter</dt>
            <dd>{e.stats?.dead_letter ?? 0}</dd>
            <dt className="text-muted">In progress</dt>
            <dd>{e.stats?.in_progress ?? 0}</dd>
            <dt className="text-muted">Cancelled</dt>
            <dd>{e.stats?.cancelled ?? 0}</dd>
          </dl>
        </Card>
      </div>
      <Card title="Delivery history">
        {deliveries.isPending && <Spinner />}
        {deliveries.rows.length === 0 && !deliveries.isPending && (
          <EmptyState title="No deliveries for this endpoint yet" />
        )}
        {deliveries.rows.length > 0 && (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>Delivery</th>
                  <th className={TH}>Event type</th>
                  <th className={TH}>Status</th>
                  <th className={TH}>Attempts</th>
                  <th className={TH}>Last attempt</th>
                  <th className={TH}>Error</th>
                </tr>
              </thead>
              <tbody>
                {deliveries.rows.map((d) => (
                  <tr key={d.id} className="border-t border-border">
                    <td className={TD}>
                      <Link className="text-accent hover:underline" to={`/deliveries/${d.id}`}>
                        {shortId(d.id)}
                      </Link>
                    </td>
                    <td className={TD}>{d.event_type}</td>
                    <td className={TD}>
                      <StatusBadge status={d.status} />
                    </td>
                    <td className={TD}>{d.attempt_count}</td>
                    <td className={TD}>{formatDateTime(d.last_attempt_at)}</td>
                    <td className={TD}>{categoryLabel(d.last_error_category)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
        {deliveries.hasNextPage && (
          <div className="pt-3">
            <Button onClick={() => void deliveries.fetchNextPage()} disabled={deliveries.isFetchingNextPage}>
              Load more
            </Button>
          </div>
        )}
      </Card>
      <ConfirmDialog
        open={confirmRotate}
        title="Rotate signing secret?"
        message="The current secret stops working immediately. Update your receiver with the new secret."
        confirmLabel="Rotate secret"
        busy={rotate.isPending}
        onCancel={() => setConfirmRotate(false)}
        onConfirm={() => rotate.mutate()}
      />
      <ConfirmDialog
        open={confirmDelete}
        title="Delete endpoint?"
        message="It will stop receiving events and its open deliveries will be cancelled."
        confirmLabel="Delete endpoint"
        busy={remove.isPending}
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() => remove.mutate()}
      />
    </>
  )
}

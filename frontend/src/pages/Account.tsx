import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { ApiError, api } from '../api/client'
import { PageHeader } from '../components/Layout'
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  CopyButton,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Spinner,
  TD,
  TH,
  TableWrap,
} from '../components/ui'
import { useAuth } from '../context/auth'
import { useCursorList } from '../hooks/useCursorList'
import type { ApiKey } from '../types/api'
import { formatDateTime, relativeTime } from '../utils/format'
import { apiKeySchema, changePasswordSchema } from '../utils/schemas'

export function ApiKeysPage() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['api-keys'], queryFn: api.listApiKeys })
  const [revealed, setRevealed] = useState<string | null>(null)
  const [toRevoke, setToRevoke] = useState<ApiKey | null>(null)
  const [serverError, setServerError] = useState<string | null>(null)
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<{ name: string }>({ resolver: zodResolver(apiKeySchema) })

  const create = useMutation({
    mutationFn: (name: string) => api.createApiKey(name),
    onSuccess: (r) => {
      setRevealed(r.key)
      setServerError(null)
      reset()
      void qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
    onError: (e) => setServerError(e instanceof ApiError ? e.message : 'Could not create the key.'),
  })
  const revoke = useMutation({
    mutationFn: (k: ApiKey) => api.revokeApiKey(k.id),
    onSuccess: () => {
      setToRevoke(null)
      void qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
  })

  return (
    <>
      <PageHeader title="API keys" subtitle="Keys let your services send events. They can do nothing else." />
      {revealed && (
        <Card title="Your new API key">
          <p className="text-sm text-warn">
            Copy this key now. For security it is shown only once and cannot be retrieved later.
          </p>
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <code
              className="break-all rounded-control bg-surface-2 px-2 py-1 font-mono text-sm"
              data-testid="api-key"
            >
              {revealed}
            </code>
            <CopyButton value={revealed} label="Copy key" />
            <Button onClick={() => setRevealed(null)}>Done</Button>
          </div>
        </Card>
      )}
      <Card title="Create a key">
        <form
          onSubmit={handleSubmit((v) => create.mutate(v.name))}
          noValidate
          className="flex flex-wrap items-start gap-3"
        >
          <div className="min-w-56 flex-1">
            <Field label="Key name" htmlFor="key-name" error={errors.name?.message}>
              <Input id="key-name" placeholder="e.g. production backend" {...register('name')} />
            </Field>
          </div>
          <Button type="submit" variant="primary" className="mt-6" disabled={create.isPending}>
            Create key
          </Button>
        </form>
        {serverError && (
          <p role="alert" className="mt-2 text-sm text-bad">
            {serverError}
          </p>
        )}
      </Card>
      <Card>
        {list.isPending && <Spinner />}
        {list.isError && <ErrorState error={list.error} onRetry={() => void list.refetch()} />}
        {list.data?.items.length === 0 && <EmptyState title="No API keys yet" />}
        {list.data && list.data.items.length > 0 && (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>Name</th>
                  <th className={TH}>Prefix</th>
                  <th className={TH}>Last used</th>
                  <th className={TH}>Created</th>
                  <th className={TH}>Status</th>
                  <th className={TH}></th>
                </tr>
              </thead>
              <tbody>
                {list.data.items.map((k) => (
                  <tr key={k.id} className="border-t border-border">
                    <td className={TD}>{k.name}</td>
                    <td className={`${TD} font-mono text-xs`}>{k.prefix}…</td>
                    <td className={TD}>{k.last_used_at ? relativeTime(k.last_used_at) : 'Never'}</td>
                    <td className={TD}>{formatDateTime(k.created_at)}</td>
                    <td className={TD}>
                      {k.revoked_at ? <Badge tone="neutral">Revoked</Badge> : <Badge tone="ok">Active</Badge>}
                    </td>
                    <td className={TD}>
                      {!k.revoked_at && (
                        <Button variant="ghost" onClick={() => setToRevoke(k)}>
                          Revoke
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
      </Card>
      <ConfirmDialog
        open={toRevoke !== null}
        title="Revoke API key?"
        message={`Requests using “${toRevoke?.name ?? ''}” will be rejected immediately. This cannot be undone.`}
        confirmLabel="Revoke key"
        busy={revoke.isPending}
        onCancel={() => setToRevoke(null)}
        onConfirm={() => toRevoke && revoke.mutate(toRevoke)}
      />
    </>
  )
}

export function ActivityPage() {
  const list = useCursorList('audit', {}, api.listAudit)
  return (
    <>
      <PageHeader title="Activity" subtitle="Who did what, and when." />
      <Card>
        {list.isPending && <Spinner />}
        {list.isError && <ErrorState error={list.error} onRetry={() => void list.refetch()} />}
        {list.isSuccess && list.rows.length === 0 && <EmptyState title="No activity yet" />}
        {list.rows.length > 0 && (
          <TableWrap>
            <table className="w-full">
              <thead>
                <tr>
                  <th className={TH}>When</th>
                  <th className={TH}>Action</th>
                  <th className={TH}>Resource</th>
                  <th className={TH}>Details</th>
                </tr>
              </thead>
              <tbody>
                {list.rows.map((a) => (
                  <tr key={a.id} className="border-t border-border">
                    <td className={TD}>{formatDateTime(a.created_at)}</td>
                    <td className={TD}>{a.action}</td>
                    <td className={`${TD} font-mono text-xs`}>
                      {a.resource_type}
                      {a.resource_id ? ` ${a.resource_id.slice(0, 8)}` : ''}
                    </td>
                    <td className={`${TD} text-muted`}>
                      {a.metadata && Object.keys(a.metadata).length > 0 ? JSON.stringify(a.metadata) : '—'}
                    </td>
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

export function SettingsPage() {
  const { user } = useAuth()
  const settings = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null)
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<{ current_password: string; new_password: string }>({
    resolver: zodResolver(changePasswordSchema),
  })
  const change = useMutation({
    mutationFn: (v: { current_password: string; new_password: string }) =>
      api.changePassword(v.current_password, v.new_password),
    onSuccess: () => {
      setMessage({ ok: true, text: 'Password changed. Other sessions were signed out.' })
      reset()
    },
    onError: (e) =>
      setMessage({ ok: false, text: e instanceof ApiError ? e.message : 'Could not change the password.' }),
  })
  const s = settings.data

  return (
    <>
      <PageHeader title="Settings" />
      <Card title="Retry defaults and limits (read-only)">
        {settings.isPending && <Spinner />}
        {settings.isError && <ErrorState error={settings.error} />}
        {s && (
          <>
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted">Max retries</dt>
              <dd>
                {s.retry.max_retries} (one initial attempt plus up to {s.retry.max_retries} retries)
              </dd>
              <dt className="text-muted">Initial retry delay</dt>
              <dd>{s.retry.initial_retry_delay_ms} ms</dd>
              <dt className="text-muted">Max retry delay</dt>
              <dd>{s.retry.max_retry_delay_ms} ms</dd>
              <dt className="text-muted">Delivery timeout</dt>
              <dd>{s.retry.delivery_timeout_ms} ms</dd>
              <dt className="text-muted">Max payload</dt>
              <dd>{Math.round(s.limits.max_payload_bytes / 1024)} KB</dd>
              <dt className="text-muted">Ingestion limit</dt>
              <dd>{s.limits.ingest_rate_per_second} events per second</dd>
            </dl>
            <p className="mt-3 text-sm text-muted">
              Each endpoint can override retries and its rate limit on its own page.
            </p>
          </>
        )}
      </Card>
      <Card title="Account">
        <p className="text-sm">
          <span className="text-muted">Signed in as </span>
          {user?.email}
        </p>
        <form
          onSubmit={handleSubmit((v) => change.mutate(v))}
          noValidate
          className="mt-4 grid max-w-md gap-4"
        >
          <Field label="Current password" htmlFor="cur" error={errors.current_password?.message}>
            <Input
              id="cur"
              type="password"
              autoComplete="current-password"
              {...register('current_password')}
            />
          </Field>
          <Field
            label="New password"
            htmlFor="new"
            error={errors.new_password?.message}
            hint="At least 10 characters"
          >
            <Input id="new" type="password" autoComplete="new-password" {...register('new_password')} />
          </Field>
          {message && (
            <p
              role={message.ok ? 'status' : 'alert'}
              className={message.ok ? 'text-sm text-ok' : 'text-sm text-bad'}
            >
              {message.text}
            </p>
          )}
          <div>
            <Button type="submit" variant="primary" disabled={change.isPending}>
              Change password
            </Button>
          </div>
        </form>
      </Card>
    </>
  )
}

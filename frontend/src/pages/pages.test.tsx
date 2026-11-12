import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp, USER } from '../test/utils'
import type { DeliveryDetail } from '../types/api'
import { ApiKeysPage } from './Account'
import { LoginPage } from './AuthPages'
import { DeliveriesPage, DeliveryDetailPage } from './Deliveries'
import { EndpointsPage } from './Endpoints'
import { EventsPage } from './Events'

afterEach(() => vi.unstubAllGlobals())

const unauthenticated = {
  'GET /api/v1/auth/me': { status: 401, json: { error: { code: 'unauthorized', message: 'Sign in' } } },
}
const authenticated = { 'GET /api/v1/auth/me': { json: { user: USER } } }

describe('login form', () => {
  it('validates before calling the API and shows server errors', async () => {
    const calls = mockApi({
      ...unauthenticated,
      'POST /api/v1/auth/login': {
        status: 401,
        json: { error: { code: 'invalid_credentials', message: 'Invalid email or password.' } },
      },
    })
    renderApp(<LoginPage />, '/login')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('Email is required')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    await user.type(screen.getByLabelText('Email'), 'a@example.com')
    await user.type(screen.getByLabelText('Password'), 'short')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('Password must be at least 10 characters')).toBeInTheDocument()

    await user.clear(screen.getByLabelText('Password'))
    await user.type(screen.getByLabelText('Password'), 'long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Invalid email or password.')
    const login = calls.find((c) => c.method === 'POST')
    expect(login?.body).toEqual({ email: 'a@example.com', password: 'long-enough-password' })
  })
})

describe('endpoint creation', () => {
  it('shows the signing secret once after creating an endpoint', async () => {
    const calls = mockApi({
      ...authenticated,
      'GET /api/v1/endpoints': { json: { items: [] } },
      'POST /api/v1/endpoints': { status: 201, json: { endpoint: { id: 'e1' }, secret: 'whsec_abc123' } },
    })
    renderApp(<EndpointsPage />, '/endpoints')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'New endpoint' }))
    await user.type(screen.getByLabelText('Name'), 'Billing')
    await user.type(screen.getByLabelText('URL'), 'https://example.com/hook')
    await user.click(screen.getByRole('button', { name: 'Create endpoint' }))

    expect(await screen.findByTestId('secret')).toHaveTextContent('whsec_abc123')
    expect(screen.getByText(/shown only once/i)).toBeInTheDocument()
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.body).toMatchObject({
      name: 'Billing',
      url: 'https://example.com/hook',
      event_types: ['*'],
      rate_limit_per_second: 10,
    })

    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByTestId('secret')).not.toBeInTheDocument()
  })

  it('rejects a URL with credentials without calling the API', async () => {
    const calls = mockApi({ ...authenticated, 'GET /api/v1/endpoints': { json: { items: [] } } })
    renderApp(<EndpointsPage />, '/endpoints')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'New endpoint' }))
    await user.type(screen.getByLabelText('Name'), 'x')
    await user.type(screen.getByLabelText('URL'), 'https://user:pw@example.com/')
    await user.click(screen.getByRole('button', { name: 'Create endpoint' }))
    expect(await screen.findByText(/without credentials/)).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })
})

describe('events explorer filters', () => {
  it('writes filters to the URL and sends them to the API', async () => {
    const calls = mockApi({
      ...authenticated,
      'GET /api/v1/events': { json: { items: [], next_cursor: null } },
    })
    renderApp(<EventsPage />, '/events')
    const user = userEvent.setup()
    await user.selectOptions(await screen.findByLabelText('Status'), 'failed')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/events?status=failed'))
    await waitFor(() => expect(calls.some((c) => c.url.includes('status=failed'))).toBe(true))
  })

  it('restores filters from the URL on load', async () => {
    const calls = mockApi({
      ...authenticated,
      'GET /api/v1/events': { json: { items: [], next_cursor: null } },
    })
    renderApp(<EventsPage />, '/events?event_type=payment.completed')
    expect(await screen.findByLabelText('Event type')).toHaveValue('payment.completed')
    await waitFor(() => expect(calls.some((c) => c.url.includes('event_type=payment.completed'))).toBe(true))
  })
})

const attempt = (n: number, trigger: 'initial' | 'retry' | 'replay', status: number) => ({
  id: `a${n}`,
  attempt_number: n,
  trigger,
  worker_id: 'w/0',
  started_at: '2026-01-01T00:00:0' + n + 'Z',
  response_status: status,
  response_body_excerpt: status >= 500 ? 'upstream exploded' : null,
  duration_ms: 12,
  error_category: status >= 500 ? 'http_server_error' : null,
  error_message: status >= 500 ? `endpoint returned HTTP ${status} (http_server_error)` : null,
})

function delivery(over: Partial<DeliveryDetail>): { delivery: DeliveryDetail } {
  return {
    delivery: {
      id: 'd1111111-0000-0000-0000-000000000000',
      event_id: 'e1111111-0000-0000-0000-000000000000',
      endpoint_id: 'p1',
      event_type: 'payment.completed',
      endpoint_name: 'Billing',
      status: 'dead_letter',
      attempt_count: 3,
      replay_count: 0,
      next_attempt_at: null,
      last_attempt_at: '2026-01-01T00:00:03Z',
      last_response_status: 500,
      last_error_category: 'http_server_error',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:03Z',
      cycle_attempt_count: 3,
      last_error: null,
      endpoint_url: 'https://example.com/hook',
      endpoint_enabled: true,
      endpoint_deleted: false,
      max_attempts: 3,
      can_replay: true,
      request_headers: [{ name: 'X-EventMesh-Attempt', value: '3' }],
      attempts: [attempt(1, 'initial', 500), attempt(2, 'retry', 500), attempt(3, 'retry', 500)],
      ...over,
    },
  }
}

function renderDelivery() {
  return renderApp(
    <Routes>
      <Route path="/deliveries/:id" element={<DeliveryDetailPage />} />
    </Routes>,
    '/deliveries/d1',
  )
}

describe('delivery detail', () => {
  it('renders the attempt timeline and a plain-language failure explanation', async () => {
    mockApi({ ...authenticated, 'GET /api/v1/deliveries/d1': { json: delivery({}) } })
    renderDelivery()
    expect(await screen.findByTestId('explanation')).toHaveTextContent(
      'The endpoint returned 500 on all 3 attempts, so the delivery was moved to dead letter.',
    )
    const items = within(screen.getByRole('list', { name: 'Attempts' })).getAllByRole('listitem')
    expect(items).toHaveLength(3)
    expect(items[0]).toHaveTextContent('Attempt 1')
    expect(items[0]).toHaveTextContent('initial')
    expect(items[0]).toHaveTextContent('upstream exploded')
    expect(screen.getByText('X-EventMesh-Attempt:')).toBeInTheDocument()
  })

  it('offers Replay only for a dead-lettered delivery and calls the API', async () => {
    const calls = mockApi({
      ...authenticated,
      'GET /api/v1/deliveries/d1': { json: delivery({}) },
      'POST /api/v1/deliveries/d1/replay': { json: delivery({ status: 'pending', can_replay: false }) },
    })
    renderDelivery()
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Replay' }))
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/deliveries/d1/replay'))).toBe(true),
    )
  })

  it('hides Replay for deliveries that are not dead letter', async () => {
    mockApi({
      ...authenticated,
      'GET /api/v1/deliveries/d1': {
        json: delivery({ status: 'delivered', can_replay: false, last_error_category: null }),
      },
    })
    renderDelivery()
    await screen.findByTestId('explanation')
    expect(screen.queryByRole('button', { name: 'Replay' })).not.toBeInTheDocument()
  })
})

describe('deliveries explorer', () => {
  it('lists deliveries with status badges', async () => {
    mockApi({
      ...authenticated,
      'GET /api/v1/endpoints': { json: { items: [] } },
      'GET /api/v1/deliveries': {
        json: { items: [delivery({}).delivery], next_cursor: null },
      },
    })
    renderApp(<DeliveriesPage />, '/deliveries')
    expect(await screen.findByText('Dead letter', { selector: 'span' })).toBeInTheDocument()
  })
})

describe('API keys', () => {
  it('shows a new key once and asks for confirmation before revoking', async () => {
    const key = {
      id: 'k1',
      name: 'prod',
      prefix: 'em_abcd1234',
      last_used_at: null,
      created_at: '2026-01-01T00:00:00Z',
      revoked_at: null,
    }
    const calls = mockApi({
      ...authenticated,
      'GET /api/v1/api-keys': { json: { items: [key] } },
      'POST /api/v1/api-keys': { status: 201, json: { api_key: key, key: 'em_abcd1234_SECRETVALUE' } },
      'DELETE /api/v1/api-keys/k1': { status: 204 },
    })
    renderApp(<ApiKeysPage />, '/api-keys')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Key name'), 'prod')
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    expect(await screen.findByTestId('api-key')).toHaveTextContent('em_abcd1234_SECRETVALUE')
    expect(screen.getByText(/shown only once/i)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByTestId('api-key')).not.toBeInTheDocument()
    expect(screen.queryByText('em_abcd1234_SECRETVALUE')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Revoke' }))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Revoke key' }))
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'DELETE' && c.url.endsWith('/api-keys/k1'))).toBe(true),
    )
  })
})

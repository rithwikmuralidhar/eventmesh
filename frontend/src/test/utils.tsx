import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactElement } from 'react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { vi } from 'vitest'
import { AuthProvider } from '../context/auth'

export interface Call {
  method: string
  url: string
  body: unknown
}

type Handler = (call: Call) => { status?: number; json?: unknown } | undefined

/**
 * Replaces fetch with a router over `handlers`, keyed "METHOD /path" (query string
 * ignored). Returns the recorded calls so tests can assert on the real requests.
 */
export function mockApi(handlers: Record<string, Handler | { status?: number; json?: unknown }>) {
  const calls: Call[] = []
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const call: Call = { method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined }
    calls.push(call)
    const key = `${method} ${url.split('?')[0]}`
    const h = handlers[key]
    const out = typeof h === 'function' ? h(call) : h
    if (!out)
      return new Response(JSON.stringify({ error: { code: 'not_found', message: `no mock for ${key}` } }), {
        status: 404,
      })
    const status = out.status ?? 200
    return new Response(status === 204 ? null : JSON.stringify(out.json ?? {}), { status })
  })
  vi.stubGlobal('fetch', fetchMock)
  return calls
}

export const USER = { id: 'u1', email: 'a@example.com', created_at: '2026-01-01T00:00:00Z' }

function LocationProbe() {
  const l = useLocation()
  return <div data-testid="location">{l.pathname + l.search}</div>
}

/** Renders inside router, query client and auth provider. Pass `/me` mocks via mockApi. */
export function renderApp(ui: ReactElement, route = '/') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[route]}>
        <AuthProvider>
          {ui}
          <LocationProbe />
        </AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

import type {
  ApiKey,
  AuditEntry,
  DeliveryDetail,
  DeliverySummary,
  Endpoint,
  EndpointInput,
  EventDetail,
  EventSummary,
  Page,
  Settings,
  Stats,
  StatsRange,
  User,
} from '../types/api'

const BASE = '/api/v1'

/** An error response from the API: `{"error": {"code", "message", "details", "request_id"}}`. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details: Record<string, unknown> | undefined
  readonly requestId: string | undefined

  constructor(
    status: number,
    code: string,
    message: string,
    details?: Record<string, unknown>,
    requestId?: string,
  ) {
    super(message)
    this.status = status
    this.code = code
    this.details = details
    this.requestId = requestId
  }
}

type Query = Record<string, string | number | undefined | null>

function withQuery(path: string, query?: Query): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== '') params.set(key, String(value))
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

async function request<T>(method: string, path: string, body?: unknown, query?: Query): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  // The custom header is the CSRF defence: a cross-site form cannot set it.
  if (method !== 'GET') headers['X-Requested-With'] = 'eventmesh-ui'

  const res = await fetch(BASE + withQuery(path, query), {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T

  const text = await res.text()
  let data: unknown
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = undefined
  }
  if (!res.ok) {
    const err = (
      data as {
        error?: { code?: string; message?: string; details?: Record<string, unknown>; request_id?: string }
      }
    )?.error
    throw new ApiError(
      res.status,
      err?.code ?? 'unknown',
      err?.message ?? `Request failed (${res.status})`,
      err?.details,
      err?.request_id,
    )
  }
  return data as T
}

export interface ListFilters {
  limit?: number
  cursor?: string | null
  [key: string]: string | number | undefined | null
}

export const api = {
  register: (email: string, password: string) =>
    request<{ user: User }>('POST', '/auth/register', { email, password }),
  login: (email: string, password: string) =>
    request<{ user: User }>('POST', '/auth/login', { email, password }),
  logout: () => request<void>('POST', '/auth/logout'),
  me: () => request<{ user: User }>('GET', '/auth/me'),
  changePassword: (current_password: string, new_password: string) =>
    request<void>('POST', '/auth/change-password', { current_password, new_password }),

  stats: (range: StatsRange) => request<Stats>('GET', '/dashboard/stats', undefined, { range }),
  settings: () => request<Settings>('GET', '/settings'),

  listEvents: (f: ListFilters) => request<Page<EventSummary>>('GET', '/events', undefined, f),
  getEvent: (id: string) => request<{ event: EventDetail }>('GET', `/events/${id}`),

  listDeliveries: (f: ListFilters) => request<Page<DeliverySummary>>('GET', '/deliveries', undefined, f),
  getDelivery: (id: string) => request<{ delivery: DeliveryDetail }>('GET', `/deliveries/${id}`),
  replayDelivery: (id: string) => request<{ delivery: DeliveryDetail }>('POST', `/deliveries/${id}/replay`),

  listEndpoints: () => request<{ items: Endpoint[] }>('GET', '/endpoints'),
  getEndpoint: (id: string) => request<{ endpoint: Endpoint }>('GET', `/endpoints/${id}`),
  createEndpoint: (input: EndpointInput) =>
    request<{ endpoint: Endpoint; secret: string }>('POST', '/endpoints', input),
  updateEndpoint: (id: string, input: Partial<EndpointInput>) =>
    request<{ endpoint: Endpoint }>('PATCH', `/endpoints/${id}`, input),
  deleteEndpoint: (id: string) => request<void>('DELETE', `/endpoints/${id}`),
  rotateSecret: (id: string) => request<{ secret: string }>('POST', `/endpoints/${id}/rotate-secret`),

  listApiKeys: () => request<{ items: ApiKey[] }>('GET', '/api-keys'),
  createApiKey: (name: string) => request<{ api_key: ApiKey; key: string }>('POST', '/api-keys', { name }),
  revokeApiKey: (id: string) => request<void>('DELETE', `/api-keys/${id}`),

  listAudit: (f: ListFilters) => request<Page<AuditEntry>>('GET', '/audit-logs', undefined, f),
}

// Shapes returned by the EventMesh API. They mirror the Go structs in backend/internal.

export const DELIVERY_STATUSES = [
  'pending',
  'processing',
  'retry_scheduled',
  'delivered',
  'dead_letter',
  'cancelled',
] as const
export type DeliveryStatus = (typeof DELIVERY_STATUSES)[number]

export const EVENT_STATUSES = ['no_subscribers', 'in_progress', 'delivered', 'failed'] as const
export type EventStatus = (typeof EVENT_STATUSES)[number]

export interface User {
  id: string
  email: string
  created_at: string
}

export interface EndpointStats {
  total: number
  delivered: number
  dead_letter: number
  in_progress: number
  cancelled: number
}

export interface Endpoint {
  id: string
  name: string
  url: string
  event_types: string[]
  enabled: boolean
  rate_limit_per_second: number
  max_retries: number | null
  initial_retry_delay_ms: number | null
  max_retry_delay_ms: number | null
  created_at: string
  updated_at: string
  stats?: EndpointStats
}

export interface EndpointInput {
  name: string
  url: string
  event_types: string[]
  enabled?: boolean
  rate_limit_per_second?: number
  max_retries?: number | null
  initial_retry_delay_ms?: number | null
  max_retry_delay_ms?: number | null
}

export interface EventSummary {
  id: string
  event_type: string
  status: EventStatus
  delivery_count: number
  idempotency_key: string | null
  created_at: string
}

export interface DeliverySummary {
  id: string
  event_id: string
  endpoint_id: string
  event_type: string
  endpoint_name: string
  status: DeliveryStatus
  attempt_count: number
  replay_count: number
  next_attempt_at: string | null
  last_attempt_at: string | null
  last_response_status: number | null
  last_error_category: string | null
  created_at: string
  updated_at: string
}

export interface EventDetail extends EventSummary {
  payload: unknown
  deliveries: DeliverySummary[]
}

export interface Attempt {
  id: string
  attempt_number: number
  trigger: 'initial' | 'retry' | 'replay'
  worker_id: string
  started_at: string
  response_status: number | null
  response_body_excerpt: string | null
  duration_ms: number
  error_category: string | null
  error_message: string | null
}

export interface DeliveryDetail extends DeliverySummary {
  cycle_attempt_count: number
  last_error: string | null
  endpoint_url: string
  endpoint_enabled: boolean
  endpoint_deleted: boolean
  max_attempts: number
  can_replay: boolean
  request_headers: { name: string; value: string }[]
  attempts: Attempt[]
}

export interface ApiKey {
  id: string
  name: string
  prefix: string
  last_used_at: string | null
  created_at: string
  revoked_at: string | null
}

export interface AuditEntry {
  id: string
  action: string
  resource_type: string
  resource_id: string | null
  metadata: Record<string, unknown> | null
  created_at: string
}

export interface StatsTotals {
  events: number
  deliveries: number
  delivered: number
  dead_letter: number
  pending: number
  cancelled: number
  success_rate: number | null
}

export interface StatsBucket {
  start: string
  total: number
  delivered: number
  dead_letter: number
}

export interface RecentFailure {
  id: string
  event_id: string
  endpoint_id: string
  event_type: string
  endpoint_name: string
  last_error_category: string | null
  last_response_status: number | null
  attempt_count: number
  last_attempt_at: string | null
  created_at: string
}

export type StatsRange = '24h' | '7d' | '30d'

export interface Stats {
  range: StatsRange
  since: string
  totals: StatsTotals
  series: StatsBucket[]
  recent_failures: RecentFailure[]
}

export interface Settings {
  retry: {
    max_retries: number
    initial_retry_delay_ms: number
    max_retry_delay_ms: number
    delivery_timeout_ms: number
  }
  limits: {
    max_payload_bytes: number
    default_endpoint_rate_per_second: number
    ingest_rate_per_second: number
    api_rate_per_second: number
  }
  ai: { docs: boolean; explain: boolean; search: boolean }
}

export interface Page<T> {
  items: T[]
  next_cursor: string | null
}

export interface AiSearchFilters {
  event_type: string | null
  status: string | null
  endpoint_id: string | null
  from: string | null
  to: string | null
  id_search: string | null
}

export interface AiExplanation {
  explanation: string
  ai_generated: boolean
  model: string
  generated_at: string
  cached: boolean
  included_excerpt: boolean
  disclaimer: string
}

export interface AiDocsAnswer {
  answer: string
  ai_generated: boolean
  sources: { source_path: string; heading_path: string; line_range: string }[]
}

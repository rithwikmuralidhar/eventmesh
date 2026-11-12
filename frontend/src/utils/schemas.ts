import { z } from 'zod'

// These schemas mirror the backend rules so users get feedback before a round trip.

export const credentialsSchema = z.object({
  email: z.string().trim().min(1, 'Email is required').email('Enter a valid email address'),
  password: z.string().min(10, 'Password must be at least 10 characters'),
})
export type CredentialsForm = z.infer<typeof credentialsSchema>

const TYPE_PATTERN = /^(\*|[a-z0-9_]+(\.[a-z0-9_]+)*(\.\*)?)$/

/** Parses a comma or newline separated list of subscription patterns. */
export function parseEventTypes(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

const optionalInt = (min: number, max: number, label: string) =>
  z
    .string()
    .trim()
    .refine(
      (v) => v === '' || (/^\d+$/.test(v) && Number(v) >= min && Number(v) <= max),
      `${label} must be a whole number from ${min} to ${max}, or empty`,
    )

export const endpointSchema = z.object({
  name: z.string().trim().min(1, 'Name is required').max(100, 'Name is at most 100 characters'),
  url: z
    .string()
    .trim()
    .min(1, 'URL is required')
    .refine((v) => {
      try {
        const u = new URL(v)
        return (u.protocol === 'http:' || u.protocol === 'https:') && !u.username && !u.password
      } catch {
        return false
      }
    }, 'Enter an http or https URL without credentials'),
  event_types: z
    .string()
    .refine((v) => parseEventTypes(v).length > 0, 'Subscribe to at least one event type')
    .refine(
      (v) => parseEventTypes(v).every((t) => TYPE_PATTERN.test(t)),
      'Use exact types (payment.completed), prefixes (payment.*) or *',
    ),
  rate_limit_per_second: z
    .string()
    .trim()
    .refine(
      (v) => /^\d+$/.test(v) && Number(v) >= 1 && Number(v) <= 1000,
      'Rate limit must be a whole number from 1 to 1000',
    ),
  max_retries: optionalInt(0, 20, 'Max retries'),
  initial_retry_delay_ms: optionalInt(1, 3_600_000, 'Initial delay'),
  max_retry_delay_ms: optionalInt(1, 86_400_000, 'Max delay'),
  enabled: z.boolean(),
})
export type EndpointForm = z.infer<typeof endpointSchema>

export const apiKeySchema = z.object({
  name: z.string().trim().min(1, 'Name is required').max(100, 'Name is at most 100 characters'),
})

export const changePasswordSchema = z
  .object({
    current_password: z.string().min(1, 'Enter your current password'),
    new_password: z.string().min(10, 'Password must be at least 10 characters'),
  })
  .refine((v) => v.current_password !== v.new_password, {
    path: ['new_password'],
    message: 'Choose a different password',
  })

export function intOrNull(v: string): number | null {
  return v.trim() === '' ? null : Number(v)
}

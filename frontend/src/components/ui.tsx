import { AlertTriangle, Check, Copy, Loader2 } from 'lucide-react'
import {
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
} from 'react'
import { ApiError } from '../api/client'
import type { DeliveryStatus, EventStatus } from '../types/api'

// Variants map to complete class strings so Tailwind can see every class.
const BUTTON_VARIANTS = {
  primary: 'bg-accent text-accent-fg hover:opacity-90',
  secondary: 'border border-border bg-surface text-text hover:bg-surface-2',
  danger: 'bg-bad text-accent-fg hover:opacity-90',
  ghost: 'text-muted hover:bg-surface-2 hover:text-text',
} as const

export function Button({
  variant = 'secondary',
  className = '',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: keyof typeof BUTTON_VARIANTS }) {
  return (
    <button
      type="button"
      {...props}
      className={`inline-flex items-center justify-center gap-2 rounded-control px-3 py-1.5 text-sm font-medium transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50 ${BUTTON_VARIANTS[variant]} ${className}`}
    />
  )
}

const INPUT_CLASS =
  'w-full rounded-control border border-border bg-surface px-3 py-1.5 text-sm text-text placeholder:text-muted focus-visible:outline-2 focus-visible:outline-accent'

export function Input({ className = '', ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`${INPUT_CLASS} ${className}`} />
}

export function Select({ className = '', ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`${INPUT_CLASS} ${className}`} />
}

export function Field({
  label,
  error,
  hint,
  children,
  htmlFor,
}: {
  label: string
  error?: string | undefined
  hint?: string
  children: ReactNode
  htmlFor?: string
}) {
  return (
    <div className="space-y-1">
      <label htmlFor={htmlFor} className="block text-sm font-medium">
        {label}
      </label>
      {children}
      {hint && !error && <p className="text-xs text-muted">{hint}</p>}
      {error && (
        <p role="alert" className="text-xs text-bad">
          {error}
        </p>
      )}
    </div>
  )
}

export function Card({
  title,
  actions,
  children,
  className = '',
}: {
  title?: string
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`min-w-0 rounded-card border border-border bg-surface ${className}`}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-2 border-b border-border px-4 py-3">
          {title && <h2 className="text-sm font-semibold">{title}</h2>}
          {actions}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

const BADGE_TONES = {
  ok: 'bg-ok-bg text-ok',
  bad: 'bg-bad-bg text-bad',
  warn: 'bg-warn-bg text-warn',
  info: 'bg-info-bg text-info',
  neutral: 'bg-neutral-bg text-muted',
} as const

export function Badge({ tone, children }: { tone: keyof typeof BADGE_TONES; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${BADGE_TONES[tone]}`}
    >
      {children}
    </span>
  )
}

const DELIVERY_BADGE: Record<DeliveryStatus, { tone: keyof typeof BADGE_TONES; label: string }> = {
  pending: { tone: 'neutral', label: 'Pending' },
  processing: { tone: 'info', label: 'Processing' },
  retry_scheduled: { tone: 'warn', label: 'Retry scheduled' },
  delivered: { tone: 'ok', label: 'Delivered' },
  dead_letter: { tone: 'bad', label: 'Dead letter' },
  cancelled: { tone: 'neutral', label: 'Cancelled' },
}

const EVENT_BADGE: Record<EventStatus, { tone: keyof typeof BADGE_TONES; label: string }> = {
  no_subscribers: { tone: 'neutral', label: 'No subscribers' },
  in_progress: { tone: 'info', label: 'In progress' },
  delivered: { tone: 'ok', label: 'Delivered' },
  failed: { tone: 'bad', label: 'Failed' },
}

/** The one place that maps a status to a colour and label. */
export function StatusBadge({ status }: { status: DeliveryStatus | EventStatus }) {
  const entry =
    (DELIVERY_BADGE as Record<string, { tone: keyof typeof BADGE_TONES; label: string }>)[status] ??
    EVENT_BADGE[status as EventStatus]
  return <Badge tone={entry.tone}>{entry.label}</Badge>
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div role="status" className="flex items-center gap-2 py-8 text-sm text-muted">
      <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> {label}…
    </div>
  )
}

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="py-10 text-center">
      <p className="text-sm font-medium">{title}</p>
      {hint && <p className="mt-1 text-sm text-muted">{hint}</p>}
    </div>
  )
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message =
    error instanceof ApiError ? error.message : 'Something went wrong. Check your connection and try again.'
  return (
    <div
      role="alert"
      className="flex items-start gap-3 rounded-card border border-border bg-bad-bg p-4 text-sm text-bad"
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
      <div className="flex-1">
        <p>{message}</p>
        {error instanceof ApiError && error.requestId && (
          <p className="mt-1 text-xs opacity-80">Request ID: {error.requestId}</p>
        )}
      </div>
      {onRetry && (
        <Button variant="secondary" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  )
}

export function CopyButton({ value, label = 'Copy' }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  return (
    <Button
      variant="secondary"
      aria-label={label}
      onClick={() => {
        void navigator.clipboard?.writeText(value)
        setCopied(true)
        window.clearTimeout(timer.current)
        timer.current = window.setTimeout(() => setCopied(false), 1500)
      }}
    >
      {copied ? <Check className="h-4 w-4" aria-hidden /> : <Copy className="h-4 w-4" aria-hidden />}
      {copied ? 'Copied' : label}
    </Button>
  )
}

/** A modal confirmation. Native <dialog> gives focus trapping and Escape for free. */
export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel,
  busy,
  onConfirm,
  onCancel,
}: {
  open: boolean
  title: string
  message: string
  confirmLabel: string
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="presentation">
      <div
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        className="w-full max-w-md rounded-card border border-border bg-surface p-5"
      >
        <h2 id="confirm-title" className="text-base font-semibold">
          {title}
        </h2>
        <p className="mt-2 text-sm text-muted">{message}</p>
        <div className="mt-5 flex justify-end gap-2">
          <Button onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={busy}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}

/** A table wrapper that scrolls on narrow screens instead of breaking the layout. */
export function TableWrap({ children }: { children: ReactNode }) {
  return <div className="overflow-x-auto">{children}</div>
}

export const TH =
  'whitespace-nowrap px-3 py-2 text-left text-xs font-medium uppercase tracking-wide text-muted'
export const TD = 'whitespace-nowrap px-3 py-2 text-sm'

import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError, api } from '../api/client'
import type { AiSearchFilters } from '../types/api'
import { Button, Card, Input } from './ui'

/** AI availability from GET /settings. While loading or on error nothing AI-related is shown. */
export function useAiAvailability() {
  const q = useQuery({ queryKey: ['settings'], queryFn: api.settings, staleTime: 60_000 })
  return q.data?.ai ?? { docs: false, explain: false, search: false }
}

function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : 'The AI request failed.'
}

/** A second, labelled block under the deterministic explanation. The page works fully without it. */
export function AiExplainCard({ deliveryId }: { deliveryId: string }) {
  const [includeExcerpt, setIncludeExcerpt] = useState(false)
  const m = useMutation({ mutationFn: () => api.aiExplain(deliveryId, includeExcerpt) })
  const ex = m.data?.explanation
  return (
    <Card title="AI suggestion (optional)">
      <p className="text-sm text-muted">
        Asks an AI model for ideas on what to check. It is sent the delivery status, the endpoint host, and
        each attempt&apos;s status code, error category and duration; never the payload, secret, signature,
        headers or full URL.
      </p>
      <label className="mt-3 flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={includeExcerpt}
          onChange={(e) => setIncludeExcerpt(e.target.checked)}
        />
        Include the last response excerpt (tokens, emails and card numbers are redacted first)
      </label>
      <div className="mt-3">
        <Button onClick={() => m.mutate()} disabled={m.isPending}>
          {m.isPending ? 'Asking…' : ex ? 'Ask again' : 'Explain with AI'}
        </Button>
      </div>
      {m.isError && (
        <p role="alert" className="mt-3 text-sm text-bad">
          {errorText(m.error)}
        </p>
      )}
      {ex && (
        <div className="mt-3 rounded-control border border-border p-3" data-testid="ai-explanation">
          <p className="text-xs font-medium uppercase tracking-wide text-muted">
            AI-generated{ex.cached ? ' (cached)' : ''}
          </p>
          <p className="mt-1 text-sm">{ex.explanation}</p>
          <p className="mt-2 text-xs text-muted">{ex.disclaimer}</p>
        </div>
      )}
    </Card>
  )
}

const FILTER_LABELS: Record<keyof AiSearchFilters, string> = {
  event_type: 'type',
  status: 'status',
  endpoint_id: 'endpoint',
  from: 'from',
  to: 'to',
  id_search: 'id',
}

/**
 * Natural-language search. The model only proposes filters; they appear as
 * removable chips and nothing is loaded until the user applies them.
 */
export function AiSearchBar({ onApply }: { onApply: (f: AiSearchFilters) => void }) {
  const [query, setQuery] = useState('')
  const [chips, setChips] = useState<AiSearchFilters | null>(null)
  const m = useMutation({
    mutationFn: () => api.aiSearchEvents(query.trim()),
    onSuccess: (r) => setChips(r.filters),
  })
  const entries = chips
    ? (Object.entries(chips) as [keyof AiSearchFilters, string | null][]).filter(([, v]) => v)
    : []

  return (
    <Card title="Search with AI (optional)">
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (query.trim()) m.mutate()
        }}
      >
        <Input
          aria-label="Describe the events you want"
          placeholder="e.g. failed payment events since 2026-11-01"
          value={query}
          maxLength={500}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Button type="submit" disabled={m.isPending || !query.trim()}>
          {m.isPending ? 'Interpreting…' : 'Interpret'}
        </Button>
      </form>
      {m.isError && (
        <p role="alert" className="mt-3 text-sm text-bad">
          {errorText(m.error)}
        </p>
      )}
      {chips && (
        <div className="mt-3" data-testid="ai-chips">
          <p className="text-xs text-muted">
            AI-interpreted filters. Remove any that are wrong, then apply. Nothing is searched until you
            apply.
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {entries.length === 0 && (
              <span className="text-sm text-muted">No filters found in that question.</span>
            )}
            {entries.map(([k, v]) => (
              <span
                key={k}
                className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5 text-sm"
              >
                {FILTER_LABELS[k]}: {v}
                <button
                  type="button"
                  aria-label={`Remove ${FILTER_LABELS[k]} filter`}
                  className="text-muted hover:text-text"
                  onClick={() => setChips({ ...chips, [k]: null })}
                >
                  ×
                </button>
              </span>
            ))}
            <Button
              variant="primary"
              disabled={entries.length === 0}
              onClick={() => {
                onApply(chips)
                setChips(null)
              }}
            >
              Apply filters
            </Button>
          </div>
        </div>
      )}
    </Card>
  )
}

/** Question box over the project documentation, with citations. */
export function AskDocsCard() {
  const [question, setQuestion] = useState('')
  const m = useMutation({ mutationFn: () => api.aiAskDocs(question.trim()) })
  return (
    <Card title="Ask the docs (optional)">
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (question.trim()) m.mutate()
        }}
      >
        <Input
          aria-label="Question about EventMesh"
          placeholder="e.g. How are failed deliveries retried?"
          value={question}
          maxLength={1000}
          onChange={(e) => setQuestion(e.target.value)}
        />
        <Button type="submit" disabled={m.isPending || !question.trim()}>
          {m.isPending ? 'Searching…' : 'Ask'}
        </Button>
      </form>
      {m.isError && (
        <p role="alert" className="mt-3 text-sm text-bad">
          {errorText(m.error)}
        </p>
      )}
      {m.data && (
        <div className="mt-3 text-sm" data-testid="docs-answer">
          <p className="text-xs font-medium uppercase tracking-wide text-muted">
            {m.data.ai_generated ? 'AI-generated from the project docs' : 'No match'}
          </p>
          <p className="mt-1 whitespace-pre-wrap">{m.data.answer}</p>
          {m.data.sources.length > 0 && (
            <ul className="mt-2 text-xs text-muted">
              {m.data.sources.map((s) => (
                <li key={`${s.source_path}:${s.line_range}`}>
                  {s.source_path} — {s.heading_path} (lines {s.line_range})
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Card>
  )
}

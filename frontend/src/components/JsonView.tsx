import type { ReactNode } from 'react'
import { CopyButton } from './ui'

// Token classes are complete strings so Tailwind detects them.
const TOKEN_CLASS = {
  key: 'text-info',
  string: 'text-ok',
  number: 'text-warn',
  literal: 'text-bad',
  punct: 'text-muted',
} as const

const TOKEN_RE =
  /("(?:\\.|[^"\\])*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}[\],:])/g

/** Splits pretty-printed JSON into coloured spans. Small enough not to need a library. */
export function highlight(json: string): ReactNode[] {
  const out: ReactNode[] = []
  let last = 0
  let i = 0
  for (const m of json.matchAll(TOKEN_RE)) {
    const index = m.index ?? 0
    if (index > last) out.push(json.slice(last, index))
    const [text, str, colon, num, lit] = m
    if (str !== undefined) {
      out.push(
        <span key={i++} className={colon ? TOKEN_CLASS.key : TOKEN_CLASS.string}>
          {str}
        </span>,
      )
      if (colon)
        out.push(
          <span key={i++} className={TOKEN_CLASS.punct}>
            {colon}
          </span>,
        )
    } else if (num !== undefined) {
      out.push(
        <span key={i++} className={TOKEN_CLASS.number}>
          {text}
        </span>,
      )
    } else if (lit !== undefined) {
      out.push(
        <span key={i++} className={TOKEN_CLASS.literal}>
          {text}
        </span>,
      )
    } else {
      out.push(
        <span key={i++} className={TOKEN_CLASS.punct}>
          {text}
        </span>,
      )
    }
    last = index + text.length
  }
  if (last < json.length) out.push(json.slice(last))
  return out
}

export function JsonView({ value }: { value: unknown }) {
  const text = JSON.stringify(value, null, 2) ?? 'null'
  return (
    <div>
      <div className="mb-2 flex justify-end">
        <CopyButton value={text} label="Copy JSON" />
      </div>
      <pre
        className="max-h-96 overflow-auto rounded-control bg-surface-2 p-3 font-mono text-xs leading-relaxed"
        data-testid="json-view"
      >
        {highlight(text)}
      </pre>
    </div>
  )
}

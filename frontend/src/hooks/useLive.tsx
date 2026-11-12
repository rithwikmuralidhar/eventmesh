import { useQueryClient } from '@tanstack/react-query'
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export type LiveStatus = 'connecting' | 'live' | 'offline'

const LiveContext = createContext<LiveStatus>('connecting')

/** Query keys refreshed by a live update. Events and deliveries lists, details, stats and endpoint statistics. */
const LIVE_KEYS = [
  ['events'],
  ['event'],
  ['deliveries'],
  ['delivery'],
  ['stats'],
  ['endpoints'],
  ['endpoint'],
]

/**
 * One EventSource per signed-in tab. Updates only invalidate queries, so there
 * is a single code path for data. On every (re)connect everything live is
 * invalidated, because updates sent during a gap are lost.
 */
export function LiveProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const [status, setStatus] = useState<LiveStatus>('connecting')

  useEffect(() => {
    const source = new EventSource('/api/v1/stream')
    const invalidate = () => {
      for (const queryKey of LIVE_KEYS) void qc.invalidateQueries({ queryKey })
    }
    source.onopen = () => {
      setStatus('live')
      invalidate()
    }
    source.onerror = () => setStatus(source.readyState === EventSource.CLOSED ? 'offline' : 'connecting')
    source.addEventListener('delivery.updated', invalidate)
    source.addEventListener('event.created', invalidate)
    // The server may also send unnamed messages; treat them the same way.
    source.onmessage = invalidate
    return () => source.close()
  }, [qc])

  return <LiveContext.Provider value={status}>{children}</LiveContext.Provider>
}

export function useLiveStatus(): LiveStatus {
  return useContext(LiveContext)
}

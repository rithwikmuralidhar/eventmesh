import { useInfiniteQuery } from '@tanstack/react-query'
import type { ListFilters } from '../api/client'
import type { Page } from '../types/api'

/** Keyset-paginated list: each "load more" passes the previous page's next_cursor. */
export function useCursorList<T>(
  key: string,
  filters: Record<string, string>,
  fetchPage: (f: ListFilters) => Promise<Page<T>>,
) {
  const q = useInfiniteQuery({
    queryKey: [key, filters],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) => fetchPage({ ...filters, limit: 25, cursor: pageParam }),
    getNextPageParam: (last) => last.next_cursor,
  })
  return { ...q, rows: q.data?.pages.flatMap((p) => p.items) ?? [] }
}

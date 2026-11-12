import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { ApiError, api } from '../api/client'
import type { User } from '../types/api'

interface AuthState {
  user: User | null
  loading: boolean
  login: (email: string, password: string) => Promise<void>
  register: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  // The session cookie survives a reload, so /auth/me restores the signed-in state.
  const me = useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      try {
        return (await api.me()).user
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null
        throw e
      }
    },
    staleTime: Infinity,
    retry: false,
  })

  const value = useMemo<AuthState>(
    () => ({
      user: me.data ?? null,
      loading: me.isPending,
      login: async (email, password) => {
        const { user } = await api.login(email, password)
        qc.setQueryData(['me'], user)
      },
      register: async (email, password) => {
        const { user } = await api.register(email, password)
        qc.setQueryData(['me'], user)
      },
      logout: async () => {
        await api.logout()
        qc.clear()
        qc.setQueryData(['me'], null)
      },
    }),
    [me.data, me.isPending, qc],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}

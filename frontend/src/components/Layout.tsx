import {
  Activity,
  Gauge,
  KeyRound,
  ListChecks,
  LogOut,
  Radio,
  Send,
  Settings as SettingsIcon,
  Webhook,
} from 'lucide-react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useAuth } from '../context/auth'
import { useLiveStatus } from '../hooks/useLive'
import { Button } from './ui'

const NAV = [
  { to: '/', label: 'Overview', icon: Gauge, end: true },
  { to: '/endpoints', label: 'Endpoints', icon: Webhook, end: false },
  { to: '/events', label: 'Events', icon: Send, end: false },
  { to: '/deliveries', label: 'Deliveries', icon: ListChecks, end: false },
  { to: '/api-keys', label: 'API keys', icon: KeyRound, end: false },
  { to: '/activity', label: 'Activity', icon: Activity, end: false },
  { to: '/settings', label: 'Settings', icon: SettingsIcon, end: false },
] as const

const LIVE_DOT = { live: 'bg-ok', connecting: 'bg-warn', offline: 'bg-bad' } as const
const LIVE_LABEL = { live: 'Live', connecting: 'Connecting…', offline: 'Offline' } as const

export function Layout() {
  const { user, logout } = useAuth()
  const live = useLiveStatus()
  const navigate = useNavigate()

  return (
    <div className="min-h-screen md:flex">
      <aside className="border-b border-border bg-surface md:w-56 md:shrink-0 md:border-b-0 md:border-r">
        <div className="flex items-center justify-between px-4 py-3 md:block">
          <span className="text-base font-semibold tracking-tight">EventMesh</span>
        </div>
        <nav aria-label="Main" className="flex gap-1 overflow-x-auto px-2 pb-2 md:flex-col md:pb-4">
          {NAV.map(({ to, label, icon: Icon, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              className={({ isActive }) =>
                `flex items-center gap-2 whitespace-nowrap rounded-control px-3 py-2 text-sm ${isActive ? 'bg-surface-2 font-medium text-text' : 'text-muted hover:bg-surface-2 hover:text-text'}`
              }
            >
              <Icon className="h-4 w-4" aria-hidden />
              {label}
            </NavLink>
          ))}
        </nav>
      </aside>
      <div className="min-w-0 flex-1">
        <header className="flex items-center justify-end gap-4 border-b border-border bg-surface px-4 py-2 text-sm">
          <span
            className="flex items-center gap-2 text-muted"
            role="status"
            aria-label={`Live updates: ${LIVE_LABEL[live]}`}
          >
            <Radio className="h-4 w-4" aria-hidden />
            <span className={`h-2 w-2 rounded-full ${LIVE_DOT[live]}`} aria-hidden />
            {LIVE_LABEL[live]}
          </span>
          <span className="hidden text-muted sm:inline">{user?.email}</span>
          <Button
            variant="ghost"
            onClick={async () => {
              await logout()
              navigate('/login')
            }}
          >
            <LogOut className="h-4 w-4" aria-hidden /> Sign out
          </Button>
        </header>
        <main className="mx-auto max-w-6xl space-y-4 p-4">
          <Outlet />
        </main>
      </div>
    </div>
  )
}

export function PageHeader({
  title,
  actions,
  subtitle,
}: {
  title: string
  actions?: React.ReactNode
  subtitle?: string
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-2">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="text-sm text-muted">{subtitle}</p>}
      </div>
      {actions}
    </div>
  )
}

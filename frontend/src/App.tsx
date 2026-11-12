import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Layout } from './components/Layout'
import { Spinner } from './components/ui'
import { useAuth } from './context/auth'
import { LiveProvider } from './hooks/useLive'
import { ActivityPage, ApiKeysPage, SettingsPage } from './pages/Account'
import { LoginPage, RegisterPage } from './pages/AuthPages'
import { DeliveriesPage, DeliveryDetailPage } from './pages/Deliveries'
import { EndpointDetailPage, EndpointsPage } from './pages/Endpoints'
import { EventDetailPage, EventsPage } from './pages/Events'
import { OverviewPage } from './pages/Overview'

/** Redirects to /login, remembering where the user was headed. */
function RequireAuth() {
  const { user, loading } = useAuth()
  const location = useLocation()
  if (loading) return <Spinner />
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  return (
    <LiveProvider>
      <Layout />
    </LiveProvider>
  )
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/register" element={<RegisterPage />} />
      <Route element={<RequireAuth />}>
        <Route index element={<OverviewPage />} />
        <Route path="endpoints" element={<EndpointsPage />} />
        <Route path="endpoints/:id" element={<EndpointDetailPage />} />
        <Route path="events" element={<EventsPage />} />
        <Route path="events/:id" element={<EventDetailPage />} />
        <Route path="deliveries" element={<DeliveriesPage />} />
        <Route path="deliveries/:id" element={<DeliveryDetailPage />} />
        <Route path="api-keys" element={<ApiKeysPage />} />
        <Route path="activity" element={<ActivityPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

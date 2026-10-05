import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { Splash } from '../components/Splash'
import { useAuth } from './AuthProvider'

export function RequireAuth() {
  const { session, loading } = useAuth()
  const location = useLocation()

  if (loading) return <Splash />
  if (!session) return <Navigate to="/login" replace state={{ from: location }} />
  return <Outlet />
}

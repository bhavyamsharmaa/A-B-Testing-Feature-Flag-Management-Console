import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { RequireAuth } from './auth/RequireAuth'
import { Background } from './components/Background'
import { Splash } from './components/Splash'
import AuditPage from './pages/AuditPage'
import ConsolePage from './pages/ConsolePage'
import DemoPage from './pages/DemoPage'
import LoginPage from './pages/LoginPage'

function RootRedirect() {
  const { session, loading } = useAuth()
  if (loading) return <Splash />
  return <Navigate to={session ? '/console' : '/login'} replace />
}

export default function App() {
  return (
    <BrowserRouter>
      <Background />
      <AuthProvider>
        <Routes>
          <Route path="/" element={<RootRedirect />} />
          <Route path="/login" element={<LoginPage />} />
          <Route path="/demo" element={<DemoPage />} />
          <Route element={<RequireAuth />}>
            <Route path="/console" element={<ConsolePage />} />
            <Route path="/console/audit" element={<AuditPage />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  )
}

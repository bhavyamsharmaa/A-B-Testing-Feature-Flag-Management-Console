import { BrowserRouter, Navigate, Outlet, Route, Routes } from 'react-router-dom'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { RequireAuth } from './auth/RequireAuth'
import { Background } from './components/Background'
import { ConfirmEmailScreen } from './components/ConfirmEmailScreen'
import { Splash } from './components/Splash'
import AuditPage from './pages/AuditPage'
import ConsolePage from './pages/ConsolePage'
import DemoPage from './pages/DemoPage'
import InvitePage from './pages/InvitePage'
import LoginPage from './pages/LoginPage'
import SettingsPage from './pages/SettingsPage'
import { WorkspaceProvider, useWorkspace } from './workspace/WorkspaceProvider'

function RootRedirect() {
  const { session, loading } = useAuth()
  if (loading) return <Splash />
  return <Navigate to={session ? '/console' : '/login'} replace />
}

/**
 * Everything under the console is remounted when the active workspace changes,
 * so no list, form, dialog or filter from the previous workspace can linger.
 */
function WorkspaceScope() {
  const { active } = useWorkspace()
  const { emailUnconfirmed } = useAuth()
  if (emailUnconfirmed) return <ConfirmEmailScreen />
  return <Outlet key={active?.id ?? 'none'} />
}

export default function App() {
  return (
    <BrowserRouter>
      <Background />
      <AuthProvider>
        <WorkspaceProvider>
        <Routes>
          <Route path="/" element={<RootRedirect />} />
          <Route path="/login" element={<LoginPage />} />
          <Route path="/demo" element={<DemoPage />} />
          <Route path="/invite/:token" element={<InvitePage />} />
          <Route element={<RequireAuth />}>
            <Route element={<WorkspaceScope />}>
              <Route path="/console" element={<ConsolePage />} />
              <Route path="/console/audit" element={<AuditPage />} />
              <Route path="/console/settings" element={<SettingsPage />} />
            </Route>
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </WorkspaceProvider>
      </AuthProvider>
    </BrowserRouter>
  )
}

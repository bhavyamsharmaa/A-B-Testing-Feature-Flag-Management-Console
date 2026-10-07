import { useMemo, useState } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { isProduction, roleIn } from './permissions'

const ENV_ORDER = ['dev', 'staging', 'production']
const STORAGE_KEY = 'helios.selectedEnv'

function readStoredEnv(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

/** Account, accessible environments and the remembered environment, shared by every console page. */
export function useConsoleEnv() {
  const { session, me, meLoading, meError, reloadMe, signOut } = useAuth()
  const email = me?.email ?? session?.user.email ?? ''

  // Environments the user has a role in: dev, staging, production, then the rest.
  const roles = useMemo(() => {
    const rank = (e: string) => (ENV_ORDER.includes(e) ? ENV_ORDER.indexOf(e) : ENV_ORDER.length)
    return [...(me?.roles ?? [])].sort((a, b) => rank(a.environment) - rank(b.environment) || a.environment.localeCompare(b.environment))
  }, [me])

  const [preferredEnv, setPreferredEnv] = useState<string | null>(readStoredEnv)
  const envNames = roles.map((r) => r.environment)
  const env = envNames.includes(preferredEnv ?? '')
    ? (preferredEnv as string)
    : envNames.includes('dev')
      ? 'dev'
      : (envNames[0] ?? null)

  function selectEnv(next: string) {
    setPreferredEnv(next)
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      /* private mode: the choice just won't persist */
    }
  }

  const role = env ? roleIn(roles, env) : null
  const prod = env ? isProduction(env) : false

  return { email, me, meLoading, meError, reloadMe, signOut, roles, env, selectEnv, role, prod }
}

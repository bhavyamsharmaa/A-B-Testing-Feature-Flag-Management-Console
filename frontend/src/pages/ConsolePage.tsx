import { useCallback, useEffect, useMemo, useState } from 'react'
import { killFlag, setFlagEnabled } from '../api/flags'
import { useAuth } from '../auth/AuthProvider'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CreateFlagModal } from '../components/CreateFlagModal'
import { EnvSwitcher } from '../components/EnvSwitcher'
import { FlagTable } from '../components/FlagTable'
import { Logo } from '../components/Logo'
import { Notice, type NoticeState } from '../components/Notice'
import { describeError } from '../lib/errors'
import { canCreate, canKill, canToggle, isProduction, roleIn, toggleDisabledReason } from '../lib/permissions'
import { useFlags } from '../lib/useFlags'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { Flag } from '../types'

const ENV_ORDER = ['dev', 'staging', 'production']
const STORAGE_KEY = 'helios.selectedEnv'

function readStoredEnv(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

type Pending = { kind: 'toggle'; flag: Flag; enable: boolean } | { kind: 'kill'; flag: Flag }

export default function ConsolePage() {
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

  const { flags, loading, error, reload, replace } = useFlags(env)
  const role = env ? roleIn(roles, env) : null
  const prod = env ? isProduction(env) : false

  const [busyKeys, setBusyKeys] = useState<Set<string>>(new Set())
  const [killedKeys, setKilledKeys] = useState<Set<string>>(new Set())
  const [notice, setNotice] = useState<NoticeState | null>(null)
  const [creating, setCreating] = useState(false)
  const [pending, setPending] = useState<Pending | null>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)

  const slow = useSlowHint(loading || busyKeys.size > 0)

  // Per-environment UI state must not leak across tabs.
  useEffect(() => {
    setNotice(null)
    setPending(null)
    setKilledKeys(new Set())
  }, [env])

  // The KILLED badge is session-only (the API has no killed state); drop it
  // once the flag is seen enabled again.
  useEffect(() => {
    if (!flags) return
    setKilledKeys((prev) => {
      const next = new Set([...prev].filter((k) => flags.some((f) => f.key === k && !f.config.enabled)))
      return next.size === prev.size ? prev : next
    })
  }, [flags])

  // Success notices fade on their own; errors stay until dismissed.
  useEffect(() => {
    if (notice?.kind !== 'success') return
    const t = setTimeout(() => setNotice(null), 7000)
    return () => clearTimeout(t)
  }, [notice])

  const setBusy = useCallback((key: string, on: boolean) => {
    setBusyKeys((prev) => {
      const next = new Set(prev)
      if (on) next.add(key)
      else next.delete(key)
      return next
    })
  }, [])

  // The UI changes only after the server confirms: each runner returns an
  // error message, or null once the server's version of the flag is applied.
  async function runToggle(flag: Flag, enable: boolean): Promise<string | null> {
    if (!env) return null
    setBusy(flag.key, true)
    try {
      const updated = await setFlagEnabled(env, flag.key, enable)
      replace(updated)
      if (enable) setKilledKeys((prev) => new Set([...prev].filter((k) => k !== flag.key)))
      setNotice({ kind: 'success', text: `${enable ? 'Enabled' : 'Disabled'} ${flag.key} in ${env}.` })
      return null
    } catch (err) {
      return describeError(err, `${enable ? 'enable' : 'disable'} flags in ${env}`)
    } finally {
      setBusy(flag.key, false)
    }
  }

  async function runKill(flag: Flag): Promise<string | null> {
    if (!env) return null
    setBusy(flag.key, true)
    try {
      const updated = await killFlag(env, flag.key)
      replace(updated)
      setKilledKeys((prev) => new Set(prev).add(flag.key))
      setNotice({ kind: 'success', text: `Killed ${flag.key} in ${env}. It is now disabled.` })
      return null
    } catch (err) {
      return describeError(err, `kill flags in ${env}`)
    } finally {
      setBusy(flag.key, false)
    }
  }

  async function onToggleClick(flag: Flag) {
    const enable = !flag.config.enabled
    if (prod) {
      setDialogError(null)
      setPending({ kind: 'toggle', flag, enable })
      return
    }
    const err = await runToggle(flag, enable)
    if (err) setNotice({ kind: 'error', text: err })
  }

  function onKillClick(flag: Flag) {
    setDialogError(null)
    setPending({ kind: 'kill', flag })
  }

  async function onConfirm() {
    if (!pending) return
    const err = pending.kind === 'kill' ? await runKill(pending.flag) : await runToggle(pending.flag, pending.enable)
    if (err) setDialogError(err)
    else setPending(null)
  }

  const dialogBusy = pending !== null && busyKeys.has(pending.flag.key)
  const toggleAllowed = env ? canToggle(role, env) : false

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      {prod && (
        <div className="mb-6 rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-2 text-center text-xs font-semibold uppercase tracking-widest text-red-300">
          Production: changes affect live users
        </div>
      )}

      <header className="flex animate-fade-up items-center justify-between">
        <h1>
          <Logo className="text-xl" />
        </h1>
        <div className="flex items-center gap-3">
          <span className="hidden text-sm text-zinc-400 sm:inline">{email}</span>
          <button
            onClick={() => void signOut()}
            className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 backdrop-blur transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
          >
            Sign out
          </button>
        </div>
      </header>

      <section
        className={`mt-8 animate-fade-up rounded-2xl border bg-surface/70 p-5 backdrop-blur-xl ${
          prod
            ? 'border-red-500/30 shadow-[0_0_80px_-30px_rgba(239,68,68,0.5)]'
            : 'border-white/10 shadow-[0_0_80px_-30px_rgba(124,92,255,0.45)]'
        }`}
        style={{ animationDelay: '0.1s' }}
      >
        {meLoading && (
          <div className="space-y-2" role="status" aria-label="Loading your access">
            <div className="h-10 animate-shimmer rounded-lg bg-white/[0.06]" />
            <div className="h-10 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: '0.15s' }} />
          </div>
        )}

        {meError && !meLoading && (
          <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            <p>{meError}</p>
            <button onClick={reloadMe} className="mt-2 underline">
              Retry
            </button>
          </div>
        )}

        {me && !meLoading && roles.length === 0 && (
          <p className="rounded-lg border border-white/10 bg-black/30 px-3 py-2.5 text-sm text-zinc-300">
            This account has no environment access yet. Ask an admin to grant you a role in an environment.
          </p>
        )}

        {env && roles.length > 0 && (
          <div className="space-y-4">
            <EnvSwitcher roles={roles} selected={env} onSelect={selectEnv} onRefresh={() => void reload()} refreshing={loading} />

            <div className="flex items-center justify-between gap-3">
              <h2 className="text-xs font-medium uppercase tracking-wider text-zinc-500">
                Flags in <span className={prod ? 'text-red-300' : 'text-zinc-300'}>{env}</span>
              </h2>
              {canCreate(role) && (
                <button
                  onClick={() => setCreating(true)}
                  className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3.5 py-1.5 text-sm font-medium text-white shadow-[0_8px_24px_-8px_rgba(124,92,255,0.8)] transition hover:brightness-110"
                >
                  Create flag
                </button>
              )}
            </div>

            {role === 'viewer' && (
              <p className="text-xs text-zinc-500">You have read-only access to {env}.</p>
            )}

            {notice && <Notice notice={notice} onDismiss={() => setNotice(null)} />}
            {slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}

            {error && !loading && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">
                  Retry
                </button>
              </div>
            )}

            {loading && flags === null && (
              <div className="space-y-2" role="status" aria-label="Loading flags">
                {[0, 1, 2].map((i) => (
                  <div key={i} className="h-14 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                ))}
              </div>
            )}

            {flags !== null && flags.length === 0 && !error && (
              <p className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400">
                No flags in this environment yet.
              </p>
            )}

            {flags !== null && flags.length > 0 && (
              <FlagTable
                flags={flags}
                busyKeys={busyKeys}
                killedKeys={killedKeys}
                toggleAllowed={toggleAllowed}
                toggleDisabledReason={toggleDisabledReason(role, env)}
                killAllowed={canKill(role)}
                onToggle={(f) => void onToggleClick(f)}
                onKill={onKillClick}
              />
            )}
          </div>
        )}
      </section>

      {creating && env && (
        <CreateFlagModal
          env={env}
          onClose={() => setCreating(false)}
          onCreated={(key) => {
            setCreating(false)
            setNotice({ kind: 'success', text: `Created ${key}. It is disabled in every environment.` })
            void reload()
          }}
        />
      )}

      {pending && env && (
        <ConfirmDialog
          title={
            pending.kind === 'kill'
              ? `Kill ${pending.flag.key} in ${env}?`
              : `${pending.enable ? 'Enable' : 'Disable'} ${pending.flag.key} in ${env}?`
          }
          confirmLabel={pending.kind === 'kill' ? 'Kill flag' : pending.enable ? 'Enable' : 'Disable'}
          tone={pending.kind === 'kill' ? 'danger' : 'primary'}
          busy={dialogBusy}
          error={dialogError}
          slowHint={dialogBusy && slow ? SLOW_HINT : null}
          onConfirm={() => void onConfirm()}
          onCancel={() => setPending(null)}
        >
          {pending.kind === 'kill' ? (
            <>
              <p>
                This immediately disables <span className="font-mono text-zinc-100">{pending.flag.key}</span> in{' '}
                <span className="font-semibold text-zinc-100">{env}</span>. Apps will serve their fallback value.
              </p>
              {!toggleAllowed && (
                <p className="text-amber-300">
                  You can't re-enable it yourself with the {role} role; an approver or admin can.
                </p>
              )}
            </>
          ) : (
            <p>
              This changes the live flag in <span className="font-semibold text-red-300">production</span> and takes
              effect for users right away.
            </p>
          )}
        </ConfirmDialog>
      )}
    </main>
  )
}

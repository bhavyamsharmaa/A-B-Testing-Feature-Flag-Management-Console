import { useCallback, useEffect, useState } from 'react'
import { killFlag, setFlagEnabled } from '../api/flags'
import { AccessGate } from '../components/AccessGate'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CreateFlagModal } from '../components/CreateFlagModal'
import { EnvSwitcher } from '../components/EnvSwitcher'
import { FlagDrawer } from '../components/FlagDrawer'
import { FlagTable } from '../components/FlagTable'
import { Notice, type NoticeState } from '../components/Notice'
import { ProductionStrip } from '../components/ProductionStrip'
import { describeError } from '../lib/errors'
import { canCreate, canKill, canToggle, toggleDisabledReason } from '../lib/permissions'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { useFlags } from '../lib/useFlags'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { Flag } from '../types'

type Pending = { kind: 'toggle'; flag: Flag; enable: boolean } | { kind: 'kill'; flag: Flag }

export default function ConsolePage() {
  const { email, me, meLoading, meError, reloadMe, signOut, roles, env, selectEnv, role, prod } = useConsoleEnv()

  const { flags, loading, error, reload, replace } = useFlags(env)

  const [busyKeys, setBusyKeys] = useState<Set<string>>(new Set())
  const [killedKeys, setKilledKeys] = useState<Set<string>>(new Set())
  const [notice, setNotice] = useState<NoticeState | null>(null)
  const [creating, setCreating] = useState(false)
  const [pending, setPending] = useState<Pending | null>(null)
  const [configuring, setConfiguring] = useState<Flag | null>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)

  const slow = useSlowHint(loading || busyKeys.size > 0)

  // Per-environment UI state must not leak across tabs.
  useEffect(() => {
    setNotice(null)
    setPending(null)
    setConfiguring(null)
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
      <ProductionStrip show={prod} />

      <ConsoleHeader email={email} onSignOut={() => void signOut()} />

      <ConsoleCard prod={prod}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />

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
                onConfigure={setConfiguring}
              />
            )}
          </div>
        )}
      </ConsoleCard>

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

      {configuring && env && (
        <FlagDrawer
          key={configuring.key}
          flag={configuring}
          env={env}
          prod={prod}
          role={role}
          canEdit={canToggle(role, env)}
          onClose={() => setConfiguring(null)}
          onSaved={replace}
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

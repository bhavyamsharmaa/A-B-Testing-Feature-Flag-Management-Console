import { useState } from 'react'
import { revokeApiToken } from '../api/tokens'
import { AccessGate } from '../components/AccessGate'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { Notice, type NoticeState } from '../components/Notice'
import { CreateTokenDialog } from '../components/tokens/CreateTokenDialog'
import { ShowTokenDialog } from '../components/tokens/ShowTokenDialog'
import { describeError } from '../lib/errors'
import { canManageWorkspace } from '../lib/permissions'
import { STATUS_MARK, environmentsText, expiryWords, lastUsedWords, utcStamp } from '../lib/tokens'
import { useApiTokens } from '../lib/useApiTokens'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { useNow } from '../lib/time'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { ApiToken } from '../types'

const btn =
  'rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40'

/** API tokens for CI pipelines and scripts. Admins and owners manage them; nobody else sees this page's content. */
export default function TokensPage() {
  const { email, me, meLoading, meError, reloadMe, signOut, workspace, workspaceRole, roles } = useConsoleEnv()
  const canManage = canManageWorkspace(workspaceRole)
  const { tokens, loading, error, reload } = useApiTokens(workspace?.id ?? null, canManage)
  const now = useNow(60_000)
  const slow = useSlowHint(loading)
  const [creating, setCreating] = useState(false)
  const [fresh, setFresh] = useState<{ token: ApiToken; plaintext: string } | null>(null)
  const [revoking, setRevoking] = useState<ApiToken | null>(null)
  const [busy, setBusy] = useState(false)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [notice, setNotice] = useState<NoticeState | null>(null)
  const revokeSlow = useSlowHint(busy)

  async function revoke() {
    if (!workspace || !revoking) return
    setBusy(true)
    setDialogError(null)
    try {
      await revokeApiToken(workspace.id, revoking.id)
      setNotice({ kind: 'success', text: `Token ${revoking.name} (${revoking.prefix}) revoked. It stops working at once.` })
      setRevoking(null)
      void reload()
    } catch (err) {
      setDialogError(describeError(err, 'revoke this token'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <ConsoleHeader email={email} onSignOut={() => void signOut()} />
      <ConsoleCard prod={false}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />
        {workspace && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className="text-xs font-medium uppercase tracking-wider text-zinc-500">API tokens in {workspace.name}</h2>
                <p className="mt-1 max-w-2xl text-xs text-zinc-500">
                  For CI pipelines and scripts that call the Helios API without a person signing in. A token never has more power than the person who
                  made it has now. Workspace id for API calls: <span className="select-all font-mono text-zinc-300">{workspace.id}</span>
                </p>
              </div>
              {canManage && (
                <div className="flex gap-2">
                  <button className={btn} onClick={() => void reload()} disabled={loading}>
                    {loading ? 'Refreshing…' : 'Refresh'}
                  </button>
                  <button
                    className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3.5 py-1.5 text-sm font-medium text-white transition hover:brightness-110"
                    onClick={() => {
                      setNotice(null)
                      setCreating(true)
                    }}
                  >
                    Create token
                  </button>
                </div>
              )}
            </div>

            {!canManage && (
              <p className="rounded-lg border border-white/10 bg-black/30 px-3 py-3 text-sm text-zinc-300" data-testid="tokens-forbidden">
                Only owners and admins of this workspace can create, see and revoke API tokens. You are {workspaceRole ?? 'a member'}.
              </p>
            )}
            {notice && <Notice notice={notice} onDismiss={() => setNotice(null)} />}

            {canManage && error && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">
                  Retry
                </button>
              </div>
            )}
            {canManage && loading && tokens === null && (
              <div className="space-y-2" role="status" aria-label="Loading API tokens">
                {[0, 1].map((i) => (
                  <div key={i} className="h-14 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                ))}
                {slow && <p className="text-xs text-zinc-500">{SLOW_HINT}</p>}
              </div>
            )}
            {canManage && tokens !== null && tokens.length === 0 && !error && (
              <div className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400" data-testid="tokens-empty">
                <p>No API tokens yet.</p>
                <p className="mt-1 text-xs text-zinc-500">Create one for a pipeline that needs to edit flags, then store it in your CI secret store.</p>
              </div>
            )}

            {canManage && tokens !== null && tokens.length > 0 && (
              <div className="relative overflow-x-auto">{/* relative: the screen-reader-only header cell must be clipped with the table */}
                <table className="w-full min-w-[860px] text-left text-sm" data-testid="tokens-table">
                  <thead>
                    <tr className="text-xs uppercase tracking-wider text-zinc-500">
                      <th className="px-3 py-2 font-medium">Token</th>
                      <th className="px-3 py-2 font-medium">Role</th>
                      <th className="px-3 py-2 font-medium">Environments</th>
                      <th className="px-3 py-2 font-medium">Expires</th>
                      <th className="px-3 py-2 font-medium">Last used</th>
                      <th className="px-3 py-2 font-medium">Created by</th>
                      <th className="px-3 py-2 font-medium">Status</th>
                      <th className="px-3 py-2 text-right font-medium">
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {tokens.map((t) => {
                      const s = STATUS_MARK[t.status]
                      return (
                        <tr key={t.id} className={`border-t border-white/5 ${t.status === 'active' ? '' : 'opacity-70'}`} data-testid={`token-${t.status}`}>
                          <td className="px-3 py-3">
                            <div className="font-medium text-zinc-100">{t.name}</div>
                            <div className="font-mono text-[11px] text-zinc-500">{t.prefix}</div>
                          </td>
                          <td className="px-3 py-3">{t.role}</td>
                          <td className="px-3 py-3">{environmentsText(t)}</td>
                          <td className="px-3 py-3">
                            <div>{expiryWords(t.expiresAt, now)}</div>
                            <div className="text-[11px] text-zinc-500">{utcStamp(t.expiresAt)}</div>
                          </td>
                          <td className="px-3 py-3" title={t.lastUsedAt ? utcStamp(t.lastUsedAt) : undefined}>
                            {lastUsedWords(t.lastUsedAt, now)}
                          </td>
                          <td className="px-3 py-3 text-zinc-300">{t.createdBy}</td>
                          <td className="px-3 py-3">
                            <span className={`whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-semibold ${t.status === 'active' ? 'bg-emerald-500/15 text-emerald-300' : t.status === 'expired' ? 'bg-amber-500/20 text-amber-200' : 'bg-white/10 text-zinc-300'}`}>
                              <span aria-hidden="true">{s.mark} </span>
                              {s.label}
                            </span>
                          </td>
                          <td className="px-3 py-3 text-right">
                            {t.status === 'active' && (
                              <button
                                className={`${btn} hover:!border-red-500/60 hover:!text-red-300`}
                                aria-label={`Revoke ${t.name} (${t.prefix})`}
                                onClick={() => {
                                  setDialogError(null)
                                  setRevoking(t)
                                }}
                              >
                                Revoke
                              </button>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}
      </ConsoleCard>

      {workspace && creating && (
        <CreateTokenDialog
          workspace={workspace}
          onClose={() => setCreating(false)}
          onCreated={(token, plaintext) => {
            setCreating(false)
            setFresh({ token, plaintext })
            void reload()
          }}
        />
      )}
      {fresh && <ShowTokenDialog token={fresh.token} plaintext={fresh.plaintext} onDone={() => setFresh(null)} />}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          confirmLabel="Revoke token"
          tone="danger"
          busy={busy}
          error={dialogError}
          slowHint={busy && revokeSlow ? SLOW_HINT : null}
          onConfirm={() => void revoke()}
          onCancel={() => setRevoking(null)}
        >
          <p>
            <span className="font-mono">{revoking.prefix}</span> stops working at once, on every server. Anything still using it (a pipeline, a script)
            will start getting refusals. This cannot be undone: create a new token to replace it.
          </p>
        </ConfirmDialog>
      )}
    </main>
  )
}

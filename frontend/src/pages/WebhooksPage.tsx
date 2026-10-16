import { useState } from 'react'
import { deleteWebhook, rotateWebhookSecret, testWebhook, updateWebhook } from '../api/webhooks'
import { AccessGate } from '../components/AccessGate'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { Notice, type NoticeState } from '../components/Notice'
import { DeliveryLog } from '../components/webhooks/DeliveryLog'
import { EndpointDialog } from '../components/webhooks/EndpointDialog'
import { ShowSecretDialog } from '../components/webhooks/ShowSecretDialog'
import { describeError } from '../lib/errors'
import { canManageWorkspace } from '../lib/permissions'
import { ENDPOINT_MARK, environmentText, eventsText, healthLine } from '../lib/webhooks'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { useWebhooks } from '../lib/useWebhooks'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { WebhookEndpoint } from '../types'

const btn =
  'rounded-lg border border-white/10 bg-white/[0.03] px-3 py-1.5 text-xs text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40'

type Confirm = { kind: 'delete' | 'rotate'; endpoint: WebhookEndpoint }

/** Webhooks: tell other systems about changes. Admins and owners manage them; nobody else sees this page's content. */
export default function WebhooksPage() {
  const { email, me, meLoading, meError, reloadMe, signOut, workspace, workspaceRole, roles } = useConsoleEnv()
  const canManage = canManageWorkspace(workspaceRole)
  const { data, loading, error, reload } = useWebhooks(workspace?.id ?? null, canManage)
  const slow = useSlowHint(loading)
  const [editing, setEditing] = useState<WebhookEndpoint | 'new' | null>(null)
  const [secret, setSecret] = useState<{ url: string; secret: string; rotated: boolean } | null>(null)
  const [logFor, setLogFor] = useState<WebhookEndpoint | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [notice, setNotice] = useState<NoticeState | null>(null)
  const confirmSlow = useSlowHint(busy === 'confirm')

  async function runConfirm() {
    if (!workspace || !confirm) return
    setBusy('confirm')
    setDialogError(null)
    try {
      if (confirm.kind === 'delete') {
        await deleteWebhook(workspace.id, confirm.endpoint.id)
        setNotice({ kind: 'success', text: `Webhook ${confirm.endpoint.url} deleted. Its queued deliveries were dropped.` })
      } else {
        const res = await rotateWebhookSecret(workspace.id, confirm.endpoint.id)
        setSecret({ url: res.endpoint.url, secret: res.secret, rotated: true })
      }
      setConfirm(null)
      void reload()
    } catch (err) {
      setDialogError(describeError(err, confirm.kind === 'delete' ? 'delete this webhook' : 'rotate the secret'))
    } finally {
      setBusy(null)
    }
  }

  async function test(e: WebhookEndpoint) {
    if (!workspace) return
    setBusy(`test-${e.id}`)
    setNotice(null)
    try {
      const d = await testWebhook(workspace.id, e.id)
      setNotice(
        d.status === 'delivered'
          ? { kind: 'success', text: `Test delivered to ${e.url} (HTTP ${d.httpStatus}, ${d.durationMs} ms).` }
          : { kind: 'error', text: `Test to ${e.url} failed: ${d.error ?? 'no answer'}${d.httpStatus ? ` (HTTP ${d.httpStatus})` : ''}. See the delivery log.` },
      )
      void reload()
    } catch (err) {
      setNotice({ kind: 'error', text: describeError(err, 'send a test') })
    } finally {
      setBusy(null)
    }
  }

  async function toggle(e: WebhookEndpoint) {
    if (!workspace) return
    setBusy(`toggle-${e.id}`)
    setNotice(null)
    try {
      await updateWebhook(workspace.id, e.id, { enabled: !e.enabled })
      setNotice({ kind: 'success', text: e.enabled ? `Webhook ${e.url} disabled: nothing more is queued for it.` : `Webhook ${e.url} enabled.` })
      void reload()
    } catch (err) {
      setNotice({ kind: 'error', text: describeError(err, 'change this webhook') })
    } finally {
      setBusy(null)
    }
  }

  const endpoints = data?.endpoints ?? null

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <ConsoleHeader email={email} onSignOut={() => void signOut()} />
      <ConsoleCard prod={false}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />
        {workspace && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className="text-xs font-medium uppercase tracking-wider text-zinc-500">Webhooks in {workspace.name}</h2>
                <p className="mt-1 max-w-2xl text-xs text-zinc-500">
                  Tell other systems when flags, experiments and scheduled changes change. Each delivery is signed, and delivered at least once.
                </p>
              </div>
              {canManage && (
                <div className="flex gap-2">
                  <button className={btn} onClick={() => void reload()} disabled={loading}>
                    {loading ? 'Refreshing…' : 'Refresh'}
                  </button>
                  <button
                    className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3.5 py-1.5 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
                    disabled={data !== null && (!data.available || data.endpoints.length >= data.limit)}
                    onClick={() => {
                      setNotice(null)
                      setEditing('new')
                    }}
                  >
                    Add webhook
                  </button>
                </div>
              )}
            </div>

            {!canManage && (
              <p className="rounded-lg border border-white/10 bg-black/30 px-3 py-3 text-sm text-zinc-300" data-testid="webhooks-forbidden">
                Only owners and admins of this workspace can add and see webhooks. You are {workspaceRole ?? 'a member'}.
              </p>
            )}
            {notice && <Notice notice={notice} onDismiss={() => setNotice(null)} />}
            {canManage && data && !data.available && (
              <p role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-100" data-testid="webhooks-unavailable">
                Webhooks are not available on this server: it has no encryption key for signing secrets (WEBHOOK_SECRET_KEY). Ask whoever runs Helios to set one.
              </p>
            )}
            {canManage && error && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">
                  Retry
                </button>
              </div>
            )}
            {canManage && loading && data === null && (
              <div className="space-y-2" role="status" aria-label="Loading webhooks">
                {[0, 1].map((i) => (
                  <div key={i} className="h-14 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                ))}
                {slow && <p className="text-xs text-zinc-500">{SLOW_HINT}</p>}
              </div>
            )}
            {canManage && endpoints !== null && endpoints.length === 0 && !error && (
              <div className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400" data-testid="webhooks-empty">
                <p>No webhooks yet.</p>
                <p className="mt-1 text-xs text-zinc-500">Add one to be told when a flag is changed or killed, or an experiment starts or stops.</p>
              </div>
            )}

            {canManage && endpoints !== null && endpoints.length > 0 && (
              <div className="relative overflow-x-auto">
                <table className="w-full min-w-[820px] text-left text-sm" data-testid="webhooks-table">
                  <thead>
                    <tr className="text-xs uppercase tracking-wider text-zinc-500">
                      <th className="px-3 py-2 font-medium">Endpoint</th>
                      <th className="px-3 py-2 font-medium">Events</th>
                      <th className="px-3 py-2 font-medium">Environment</th>
                      <th className="px-3 py-2 font-medium">Health</th>
                      <th className="px-3 py-2 text-right font-medium">
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {endpoints.map((e) => {
                      const m = ENDPOINT_MARK[e.status]
                      return (
                        <tr key={e.id} className={`border-t border-white/5 align-top ${e.enabled ? '' : 'opacity-80'}`} data-testid={`webhook-${e.status}`}>
                          <td className="px-3 py-3">
                            <div className="break-all font-medium text-zinc-100">{e.url}</div>
                            {e.description && <div className="text-xs text-zinc-400">{e.description}</div>}
                            <div className="mt-0.5 font-mono text-[11px] text-zinc-500">
                              secret {e.secretHint}
                              {e.previousSecretExpiresAt && ' · old secret still signing'}
                            </div>
                          </td>
                          <td className="px-3 py-3 text-zinc-300" title={e.eventTypes.join(', ')}>
                            {eventsText(e.eventTypes, data?.eventTypes.length ?? 9)}
                          </td>
                          <td className="px-3 py-3">{environmentText(e)}</td>
                          <td className="px-3 py-3">
                            <span className={`whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-semibold ${m.tone}`}>
                              <span aria-hidden="true">{m.mark} </span>
                              {m.label}
                            </span>
                            <div className="mt-1 max-w-[16rem] text-xs text-zinc-400">{healthLine(e)}</div>
                          </td>
                          <td className="px-3 py-3 text-right">
                            <div className="flex flex-wrap justify-end gap-1.5">
                              <button className={btn} onClick={() => setLogFor(e)} aria-label={`Deliveries for ${e.url}`}>
                                Deliveries
                              </button>
                              <button className={btn} disabled={busy !== null} onClick={() => void test(e)} aria-label={`Send test to ${e.url}`}>
                                {busy === `test-${e.id}` ? 'Sending…' : 'Send test'}
                              </button>
                              <button className={btn} disabled={busy !== null} onClick={() => void toggle(e)} aria-label={`${e.enabled ? 'Disable' : 'Enable'} ${e.url}`}>
                                {e.enabled ? 'Disable' : 'Enable'}
                              </button>
                              <button className={btn} onClick={() => setEditing(e)} aria-label={`Edit ${e.url}`}>
                                Edit
                              </button>
                              <button
                                className={btn}
                                onClick={() => {
                                  setDialogError(null)
                                  setConfirm({ kind: 'rotate', endpoint: e })
                                }}
                                aria-label={`Rotate secret of ${e.url}`}
                              >
                                Rotate secret
                              </button>
                              <button
                                className={`${btn} hover:!border-red-500/60 hover:!text-red-300`}
                                onClick={() => {
                                  setDialogError(null)
                                  setConfirm({ kind: 'delete', endpoint: e })
                                }}
                                aria-label={`Delete ${e.url}`}
                              >
                                Delete
                              </button>
                            </div>
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

      {workspace && editing && data && (
        <EndpointDialog
          workspace={workspace}
          eventTypes={data.eventTypes}
          endpoint={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={(endpoint, sec) => {
            setEditing(null)
            if (sec) setSecret({ url: endpoint.url, secret: sec, rotated: false })
            else setNotice({ kind: 'success', text: `Webhook ${endpoint.url} saved.` })
            void reload()
          }}
        />
      )}
      {secret && <ShowSecretDialog url={secret.url} secret={secret.secret} rotated={secret.rotated} onDone={() => setSecret(null)} />}
      {workspace && logFor && <DeliveryLog workspaceId={workspace.id} endpoint={logFor} onClose={() => setLogFor(null)} onChanged={() => void reload()} />}
      {confirm && (
        <ConfirmDialog
          title={confirm.kind === 'delete' ? 'Delete this webhook?' : 'Rotate the signing secret?'}
          confirmLabel={confirm.kind === 'delete' ? 'Delete webhook' : 'Rotate secret'}
          tone={confirm.kind === 'delete' ? 'danger' : 'primary'}
          busy={busy === 'confirm'}
          error={dialogError}
          slowHint={busy === 'confirm' && confirmSlow ? SLOW_HINT : null}
          onConfirm={() => void runConfirm()}
          onCancel={() => setConfirm(null)}
        >
          {confirm.kind === 'delete' ? (
            <p>
              <span className="break-all font-mono">{confirm.endpoint.url}</span> stops receiving events, and everything still queued for it is dropped. This cannot
              be undone.
            </p>
          ) : (
            <p>
              A new secret is made and shown once. The current one keeps signing for 24 hours (each delivery carries both signatures), so you can update the
              receiver without missing events.
            </p>
          )}
        </ConfirmDialog>
      )}
    </main>
  )
}

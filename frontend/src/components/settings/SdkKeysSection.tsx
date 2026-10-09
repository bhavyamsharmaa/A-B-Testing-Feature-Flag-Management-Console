import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { ApiError } from '../../api/client'
import { createSdkKey, listSdkKeys, revokeSdkKey } from '../../api/workspaces'
import { canManageWorkspace } from '../../lib/permissions'
import type { SdkKey, Workspace } from '../../types'
import { ConfirmDialog } from '../ConfirmDialog'
import { CopyField } from './CopyField'

const fmt = (iso: string) => new Date(iso).toLocaleDateString(undefined, { dateStyle: 'medium' })

/** SDK keys of one environment of the workspace. Admins and owners only. */
export function SdkKeysSection({ workspace }: { workspace: Workspace }) {
  const canManage = canManageWorkspace(workspace.role)
  const [env, setEnv] = useState(workspace.environments.find((e) => e.key === 'dev')?.key ?? workspace.environments[0]?.key ?? '')
  const [keys, setKeys] = useState<SdkKey[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [creating, setCreating] = useState(false)
  const [fresh, setFresh] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<SdkKey | null>(null)
  const [busy, setBusy] = useState(false)
  const [dialogError, setDialogError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!canManage || !env) return
    setError(null)
    try {
      setKeys(await listSdkKeys(env))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not load SDK keys.')
    }
  }, [canManage, env])

  useEffect(() => {
    setKeys(null)
    setFresh(null)
    void load()
  }, [load])

  if (!canManage) {
    return (
      <section aria-labelledby="keys-heading" className="space-y-2">
        <h2 id="keys-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
          SDK keys
        </h2>
        <p className="text-sm text-zinc-500">Only owners and admins can see and manage SDK keys.</p>
      </section>
    )
  }

  async function onCreate(e: FormEvent) {
    e.preventDefault()
    setCreating(true)
    setError(null)
    setFresh(null)
    try {
      const res = await createSdkKey(env, name.trim())
      setFresh(res.plaintext)
      setName('')
      await load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not create the key.')
    } finally {
      setCreating(false)
    }
  }

  async function onRevoke() {
    if (!revoking) return
    setBusy(true)
    setDialogError(null)
    try {
      await revokeSdkKey(env, revoking.id)
      setRevoking(null)
      await load()
    } catch (err) {
      setDialogError(err instanceof ApiError ? err.message : 'Could not revoke the key.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-labelledby="keys-heading" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="keys-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
          SDK keys
        </h2>
        <label className="flex items-center gap-2 text-sm text-zinc-400">
          Environment
          <select
            value={env}
            onChange={(e) => setEnv(e.target.value)}
            className="rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 font-mono text-sm outline-none focus:border-accent"
          >
            {workspace.environments.map((e) => (
              <option key={e.id} value={e.key}>
                {e.key}
              </option>
            ))}
          </select>
        </label>
      </div>
      <p className="text-xs text-zinc-500">
        A key reads this workspace's flags in one environment, and nothing from any other workspace. Revoking takes effect within a minute.
      </p>

      <form onSubmit={onCreate} className="flex flex-wrap items-end gap-3">
        <div className="min-w-[14rem] flex-1">
          <label htmlFor="key-name" className="block text-sm text-zinc-300">
            Label (optional)
          </label>
          <input
            id="key-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={60}
            placeholder="web app"
            disabled={creating}
            className="mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:opacity-60"
          />
        </div>
        <button
          type="submit"
          disabled={creating}
          className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {creating ? 'Creating…' : `Create key for ${env}`}
        </button>
      </form>

      {error && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {error}
        </p>
      )}

      {fresh && (
        <div role="status" data-testid="new-sdk-key" className="space-y-2 rounded-lg border border-emerald-500/40 bg-emerald-500/10 p-3 text-sm">
          <p className="text-emerald-200">Copy this key now. It is shown once and only its hash is stored.</p>
          <CopyField value={fresh} label="New SDK key" />
        </div>
      )}

      {keys && keys.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[520px] text-left text-sm" data-testid="sdk-keys-table">
            <tbody>
              {keys.map((k) => (
                <tr key={k.id} className={`border-t border-white/5 ${k.revokedAt ? 'opacity-50' : ''}`}>
                  <td className="px-3 py-2.5">
                    <span className="font-mono text-[13px] text-zinc-100">{k.prefix}…</span>
                    {k.name && <span className="ml-2 text-xs text-zinc-400">{k.name}</span>}
                  </td>
                  <td className="px-3 py-2.5 text-xs text-zinc-400">Created {fmt(k.createdAt)}</td>
                  <td className="px-3 py-2.5 text-right">
                    {k.revokedAt ? (
                      <span className="text-xs text-zinc-500">Revoked</span>
                    ) : (
                      <button
                        onClick={() => {
                          setDialogError(null)
                          setRevoking(k)
                        }}
                        className="rounded-lg border border-red-500/50 bg-red-500/10 px-3 py-1.5 text-xs font-medium text-red-300 transition hover:bg-red-600 hover:text-white"
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {keys && keys.length === 0 && <p className="text-sm text-zinc-500">No SDK keys in {env} yet.</p>}

      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.prefix}…?`}
          confirmLabel="Revoke key"
          tone="danger"
          busy={busy}
          error={dialogError}
          slowHint={null}
          onConfirm={() => void onRevoke()}
          onCancel={() => setRevoking(null)}
        >
          <p>Apps using this key stop receiving flag values (they fall back to defaults) within a minute.</p>
        </ConfirmDialog>
      )}
    </section>
  )
}

import { useState } from 'react'
import { createApiToken } from '../../api/tokens'
import { describeError } from '../../lib/errors'
import { DEFAULT_EXPIRY_DAYS, EXPIRY_DAYS, TOKEN_ROLE_HINT, assignableTokenRoles, utcStamp } from '../../lib/tokens'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import type { ApiToken, ApiTokenRole, Workspace } from '../../types'
import { Modal } from '../Modal'
import { field } from '../TargetingEditor'

interface Props {
  workspace: Workspace
  onClose: () => void
  /** The token and its plaintext, once. */
  onCreated: (token: ApiToken, plaintext: string) => void
}

export function CreateTokenDialog({ workspace, onClose, onCreated }: Props) {
  const roles = assignableTokenRoles(workspace.role)
  const [name, setName] = useState('')
  const [role, setRole] = useState<ApiTokenRole>('editor')
  const [limit, setLimit] = useState(false)
  const [envs, setEnvs] = useState<string[]>([])
  const [days, setDays] = useState<number>(DEFAULT_EXPIRY_DAYS)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showErrors, setShowErrors] = useState(false)
  const slow = useSlowHint(busy)

  const nameProblem = name.trim() === '' ? 'Name is required.' : name.trim().length > 60 ? 'Name is at most 60 characters.' : null
  const envProblem = limit && envs.length === 0 ? 'Choose at least one environment, or allow all of them.' : null
  const expires = new Date(Date.now() + days * 86_400_000).toISOString()

  async function submit() {
    setShowErrors(true)
    setError(null)
    if (nameProblem || envProblem) return
    setBusy(true)
    try {
      const res = await createApiToken(workspace.id, { name: name.trim(), role, environments: limit ? envs : [], expiresInDays: days })
      onCreated(res.token, res.plaintext)
    } catch (err) {
      setError(describeError(err, 'create API tokens'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Create an API token" onClose={onClose} busy={busy} wide>
      <div className="mt-4 space-y-5">
        <p className="text-sm text-zinc-400">
          A token lets a CI pipeline or a script call the Helios API without a person signing in. It acts with the role you give it, but never more
          than <strong className="font-semibold text-zinc-200">you</strong> could: if your own role is lowered or you leave the workspace, the token
          loses that power at once.
        </p>
        <div>
          <label htmlFor="tok-name" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Name
          </label>
          <input id="tok-name" className={`${field} mt-1 w-full`} value={name} maxLength={60} autoFocus disabled={busy} placeholder="deploy pipeline" onChange={(e) => setName(e.target.value)} />
          <p className="mt-1 text-xs text-zinc-500">Shown in the list and in the audit log next to what the token did.</p>
          {showErrors && nameProblem && <p className="mt-1 text-xs text-red-400">{nameProblem}</p>}
        </div>

        <div>
          <label htmlFor="tok-role" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Role
          </label>
          <select id="tok-role" className={`${field} mt-1 w-full sm:w-64`} value={role} disabled={busy} onChange={(e) => setRole(e.target.value as ApiTokenRole)}>
            {roles.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
          <p className="mt-1 text-xs text-zinc-400">{TOKEN_ROLE_HINT[role]}. You can only give a role up to your own ({workspace.role}); a token is never an owner, and its role cannot be raised later: create a new token instead.</p>
        </div>

        <fieldset disabled={busy}>
          <legend className="text-xs font-medium uppercase tracking-wider text-zinc-500">Environments</legend>
          <label className="mt-2 flex items-center gap-2 text-sm">
            <input type="radio" name="tok-scope" checked={!limit} onChange={() => setLimit(false)} />
            All environments of this workspace
          </label>
          <label className="mt-1 flex items-center gap-2 text-sm">
            <input type="radio" name="tok-scope" checked={limit} onChange={() => setLimit(true)} />
            Only these:
          </label>
          {limit && (
            <div className="ml-6 mt-2 space-y-1">
              {workspace.environments.map((e) => (
                <label key={e.id} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={envs.includes(e.key)}
                    onChange={(ev) => setEnvs(ev.target.checked ? [...envs, e.key] : envs.filter((k) => k !== e.key))}
                  />
                  <span className="font-mono">{e.key}</span>
                  {e.isProduction && <span className="text-xs text-zinc-400">(production)</span>}
                </label>
              ))}
              <p className="text-xs text-zinc-500">The token sees no other environment (it gets &ldquo;not found&rdquo;) and cannot change workspace-level things such as segments.</p>
            </div>
          )}
          {showErrors && envProblem && <p className="mt-1 text-xs text-red-400">{envProblem}</p>}
        </fieldset>

        <div>
          <label htmlFor="tok-days" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Expires
          </label>
          <select id="tok-days" className={`${field} mt-1 w-full sm:w-64`} value={days} disabled={busy} onChange={(e) => setDays(Number(e.target.value))}>
            {EXPIRY_DAYS.map((d) => (
              <option key={d} value={d}>
                in {d} days{d === DEFAULT_EXPIRY_DAYS ? ' (default)' : d === 366 ? ' (the longest)' : ''}
              </option>
            ))}
          </select>
          <p className="mt-1 text-xs text-zinc-400" data-testid="token-expiry-line">
            It stops working on about {utcStamp(expires)}. Tokens always expire, at most a year after they are made.
          </p>
        </div>

        {error && (
          <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}
        {busy && slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}

        <div className="flex justify-end gap-2 border-t border-white/5 pt-4">
          <button onClick={onClose} disabled={busy} className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50">
            Cancel
          </button>
          <button
            onClick={() => void submit()}
            disabled={busy}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy ? 'Creating…' : 'Create token'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

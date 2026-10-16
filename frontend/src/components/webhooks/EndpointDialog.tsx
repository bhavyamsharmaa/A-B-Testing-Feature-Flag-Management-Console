import { useState } from 'react'
import { createWebhook, updateWebhook } from '../../api/webhooks'
import { describeError } from '../../lib/errors'
import { EVENT_LABEL, urlProblem } from '../../lib/webhooks'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import type { WebhookEndpoint, WebhookEventType, Workspace } from '../../types'
import { Modal } from '../Modal'
import { field } from '../TargetingEditor'

interface Props {
  workspace: Workspace
  eventTypes: WebhookEventType[]
  /** Editing this endpoint; omitted: creating. */
  endpoint?: WebhookEndpoint
  onClose: () => void
  /** The endpoint, and for a new one its secret (shown once). */
  onSaved: (endpoint: WebhookEndpoint, secret: string | null) => void
}

export function EndpointDialog({ workspace, eventTypes, endpoint, onClose, onSaved }: Props) {
  const [url, setUrl] = useState(endpoint?.url ?? '')
  const [description, setDescription] = useState(endpoint?.description ?? '')
  const [types, setTypes] = useState<WebhookEventType[]>(endpoint?.eventTypes ?? eventTypes)
  const [envId, setEnvId] = useState<string>(endpoint?.environment?.id ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showErrors, setShowErrors] = useState(false)
  const slow = useSlowHint(busy)

  const urlErr = urlProblem(url)
  const typeErr = types.length === 0 ? 'Choose at least one event.' : null
  const descErr = description.length > 200 ? 'Description is at most 200 characters.' : null

  async function submit() {
    setShowErrors(true)
    setError(null)
    if (urlErr || typeErr || descErr) return
    setBusy(true)
    try {
      const input = { url: url.trim(), description: description.trim(), eventTypes: types, environmentId: envId || null }
      if (endpoint) onSaved(await updateWebhook(workspace.id, endpoint.id, input), null)
      else {
        const res = await createWebhook(workspace.id, input)
        onSaved(res.endpoint, res.secret)
      }
    } catch (err) {
      setError(describeError(err, 'save this webhook'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={endpoint ? 'Edit webhook' : 'Add a webhook'} onClose={onClose} busy={busy} wide>
      <div className="mt-4 space-y-5">
        <p className="text-sm text-zinc-400">
          Helios sends a signed HTTPS POST to this address when the events you choose happen. The address must be public: Helios refuses private
          networks, localhost and cloud metadata addresses, and follows no redirects.
        </p>
        <div>
          <label htmlFor="wh-url" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            URL
          </label>
          <input id="wh-url" className={`${field} mt-1 w-full`} value={url} disabled={busy} autoFocus placeholder="https://example.com/hooks/helios" onChange={(e) => setUrl(e.target.value)} />
          {showErrors && urlErr && <p className="mt-1 text-xs text-red-400">{urlErr}</p>}
        </div>
        <div>
          <label htmlFor="wh-desc" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Description <span className="normal-case text-zinc-600">(optional)</span>
          </label>
          <input id="wh-desc" className={`${field} mt-1 w-full`} value={description} maxLength={200} disabled={busy} placeholder="Slack bridge" onChange={(e) => setDescription(e.target.value)} />
          {showErrors && descErr && <p className="mt-1 text-xs text-red-400">{descErr}</p>}
        </div>
        <fieldset disabled={busy}>
          <legend className="text-xs font-medium uppercase tracking-wider text-zinc-500">Events</legend>
          <div className="mt-2 flex gap-3 text-xs">
            <button type="button" className="underline text-zinc-400" onClick={() => setTypes(eventTypes)}>
              Select all
            </button>
            <button type="button" className="underline text-zinc-400" onClick={() => setTypes([])}>
              Clear
            </button>
          </div>
          <div className="mt-2 grid gap-1 sm:grid-cols-2">
            {eventTypes.map((t) => (
              <label key={t} className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={types.includes(t)} onChange={(e) => setTypes(e.target.checked ? [...types, t] : types.filter((x) => x !== t))} />
                <span>{EVENT_LABEL[t] ?? t}</span>
                <span className="font-mono text-[11px] text-zinc-500">{t}</span>
              </label>
            ))}
          </div>
          {showErrors && typeErr && <p className="mt-1 text-xs text-red-400">{typeErr}</p>}
        </fieldset>
        <div>
          <label htmlFor="wh-env" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Environment
          </label>
          <select id="wh-env" className={`${field} mt-1 w-full sm:w-64`} value={envId} disabled={busy} onChange={(e) => setEnvId(e.target.value)}>
            <option value="">All environments</option>
            {workspace.environments.map((e) => (
              <option key={e.id} value={e.id}>
                {e.key}
              </option>
            ))}
          </select>
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
            {busy ? 'Saving…' : endpoint ? 'Save changes' : 'Add webhook'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

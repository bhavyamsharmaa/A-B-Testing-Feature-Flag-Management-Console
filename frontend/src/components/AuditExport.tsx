import { useState } from 'react'
import { ApiError } from '../api/client'
import { exportAuditLog } from '../api/audit'

const day = (offsetDays: number) => new Date(Date.now() + offsetDays * 86_400_000).toISOString().slice(0, 10)

/** Admins and owners: download the environment's audit log as CSV or JSON for up to 90 days. The export is itself audited. */
export function AuditExport({ env }: { env: string }) {
  const [open, setOpen] = useState(false)
  const [format, setFormat] = useState<'csv' | 'json'>('csv')
  const [from, setFrom] = useState(day(-30))
  const [to, setTo] = useState(day(0))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)

  const days = Math.round((Date.parse(to) - Date.parse(from)) / 86_400_000) + 1
  const rangeProblem = !from || !to ? 'Choose both dates.' : Date.parse(from) > Date.parse(to) ? 'The first date must not be after the last.' : days > 90 ? 'Choose at most 90 days.' : null

  async function run() {
    setBusy(true)
    setError(null)
    setDone(null)
    try {
      const { blob, filename } = await exportAuditLog(env, { format, from, to })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      a.remove()
      setTimeout(() => URL.revokeObjectURL(url), 10_000)
      setDone(`Downloaded ${filename}.`)
    } catch (err) {
      setError(err instanceof ApiError ? (err.retryAfter ? `${err.message} (try again in ${err.retryAfter} s)` : err.message) : 'The export failed. Try again.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="audit-export">
      <button
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
      >
        Export…
      </button>
      {open && (
        <div className="mt-3 space-y-3 rounded-lg border border-white/10 bg-black/30 p-3">
          <p className="text-xs text-zinc-500">
            Downloads this environment&rsquo;s entries (and the workspace-wide ones) for the dates below, up to 90 days and 100,000 entries. Secrets are never included. Each export is recorded in the audit log.
          </p>
          <div className="flex flex-wrap items-end gap-3">
            <div>
              <label htmlFor="export-from" className="block text-xs text-zinc-400">
                From
              </label>
              <input id="export-from" type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} disabled={busy} className="mt-1 rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm" />
            </div>
            <div>
              <label htmlFor="export-to" className="block text-xs text-zinc-400">
                To (including this day)
              </label>
              <input id="export-to" type="date" value={to} min={from} onChange={(e) => setTo(e.target.value)} disabled={busy} className="mt-1 rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm" />
            </div>
            <div>
              <label htmlFor="export-format" className="block text-xs text-zinc-400">
                Format
              </label>
              <select id="export-format" value={format} onChange={(e) => setFormat(e.target.value as 'csv' | 'json')} disabled={busy} className="mt-1 rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm">
                <option value="csv">CSV (spreadsheet)</option>
                <option value="json">JSON</option>
              </select>
            </div>
            <button
              onClick={() => void run()}
              disabled={busy || rangeProblem !== null}
              className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {busy ? 'Preparing…' : 'Download'}
            </button>
          </div>
          {rangeProblem && <p className="text-xs text-red-300">{rangeProblem}</p>}
          {error && (
            <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
              {error}
            </p>
          )}
          {done && (
            <p role="status" className="text-sm text-emerald-300">
              {done}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

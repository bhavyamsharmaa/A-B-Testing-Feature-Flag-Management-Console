import { useCallback, useEffect, useRef, useState } from 'react'
import { listDeliveries, resendDelivery } from '../../api/webhooks'
import { describeError } from '../../lib/errors'
import { DELIVERY_MARK, canResend, durationText, nextTryText } from '../../lib/webhooks'
import type { WebhookDelivery, WebhookEndpoint } from '../../types'
import { Modal } from '../Modal'

const btn =
  'rounded-lg border border-white/10 bg-white/[0.03] px-3 py-1.5 text-xs text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40'

const stamp = (iso: string | null) => (iso ? new Date(iso).toISOString().replace('T', ' ').slice(0, 19) + ' UTC' : '–')

/** The delivery log of one endpoint: what was sent, what came back, and a resend for failures. */
export function DeliveryLog({ workspaceId, endpoint, onClose, onChanged }: { workspaceId: string; endpoint: WebhookEndpoint; onClose: () => void; onChanged: () => void }) {
  const [rows, setRows] = useState<WebhookDelivery[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [resending, setResending] = useState<string | null>(null)
  const seq = useRef(0)

  const load = useCallback(async () => {
    const id = ++seq.current
    setError(null)
    try {
      const list = await listDeliveries(workspaceId, endpoint.id)
      if (id === seq.current) setRows(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view deliveries'))
    }
  }, [workspaceId, endpoint.id])

  useEffect(() => {
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  async function resend(d: WebhookDelivery) {
    setResending(d.id)
    setError(null)
    try {
      await resendDelivery(workspaceId, endpoint.id, d.id)
      await load()
      onChanged()
    } catch (err) {
      setError(describeError(err, 'resend this delivery'))
    } finally {
      setResending(null)
    }
  }

  return (
    <Modal title={`Deliveries to ${endpoint.url}`} onClose={onClose} wide>
      <div className="mt-4 space-y-3">
        <p className="text-xs text-zinc-500">
          The newest 50. Delivery is at least once: a retry carries the same delivery id, so a receiver can ignore repeats. Failed deliveries are retried
          for about a day, then given up on.
        </p>
        {error && (
          <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}{' '}
            <button className="underline" onClick={() => void load()}>
              Retry
            </button>
          </p>
        )}
        {rows === null && !error && (
          <div role="status" aria-label="Loading deliveries" className="space-y-2">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-9 animate-shimmer rounded bg-white/[0.06]" />
            ))}
          </div>
        )}
        {rows !== null && rows.length === 0 && (
          <p className="rounded-lg border border-dashed border-white/10 px-4 py-8 text-center text-sm text-zinc-400" data-testid="deliveries-empty">
            Nothing has been sent to this endpoint yet. Use &ldquo;Send test&rdquo; to try it.
          </p>
        )}
        {rows !== null && rows.length > 0 && (
          <div className="relative overflow-x-auto">
            <table className="w-full min-w-[720px] text-left text-sm" data-testid="deliveries-table">
              <thead>
                <tr className="text-xs uppercase tracking-wider text-zinc-500">
                  <th className="px-3 py-2 font-medium">Event</th>
                  <th className="px-3 py-2 font-medium">Result</th>
                  <th className="px-3 py-2 font-medium">HTTP</th>
                  <th className="px-3 py-2 font-medium">Time</th>
                  <th className="px-3 py-2 font-medium">Attempts</th>
                  <th className="px-3 py-2 font-medium">When</th>
                  <th className="px-3 py-2 text-right font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((d) => {
                  const m = DELIVERY_MARK[d.status]
                  return (
                    <tr key={d.id} className="border-t border-white/5 align-top" data-testid={`delivery-${d.status}`}>
                      <td className="px-3 py-2.5">
                        <div className="font-mono text-xs text-zinc-200">{d.eventType}</div>
                        <div className="font-mono text-[10px] text-zinc-500" title="The delivery id the receiver sees">
                          {d.id.slice(0, 8)}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <span className={`whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-semibold ${m.tone}`}>
                          <span aria-hidden="true">{m.mark} </span>
                          {m.label}
                        </span>
                        {d.error && <div className="mt-1 max-w-xs text-xs text-zinc-400">{d.error}</div>}
                        {d.responseHint && <div className="mt-1 max-w-xs truncate font-mono text-[11px] text-zinc-500">{d.responseHint}</div>}
                        {nextTryText(d) && <div className="mt-1 text-[11px] text-zinc-500">{nextTryText(d)}</div>}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-xs">{d.httpStatus ?? '–'}</td>
                      <td className="px-3 py-2.5 text-xs">{durationText(d.durationMs)}</td>
                      <td className="px-3 py-2.5 text-xs">{d.attempts}</td>
                      <td className="px-3 py-2.5 text-xs text-zinc-400">{stamp(d.lastAttemptAt ?? d.createdAt)}</td>
                      <td className="px-3 py-2.5 text-right">
                        {canResend(d) && (
                          <button className={btn} disabled={resending !== null} aria-label={`Resend ${d.eventType} ${d.id.slice(0, 8)}`} onClick={() => void resend(d)}>
                            {resending === d.id ? 'Sending…' : 'Resend'}
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
        <div className="flex justify-between border-t border-white/5 pt-3">
          <button className={btn} onClick={() => void load()}>
            Refresh
          </button>
          <button className={btn} onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </Modal>
  )
}

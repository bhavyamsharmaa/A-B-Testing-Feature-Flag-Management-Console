import { useEffect, useState } from 'react'
import { deleteSegment, segmentUsage } from '../../api/segments'
import { describeError } from '../../lib/errors'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import type { SegmentSummary, SegmentUsage } from '../../types'
import { ConfirmDialog } from '../ConfirmDialog'

interface Props {
  workspaceId: string
  segment: SegmentSummary
  onClose: () => void
  onDeleted: () => void
}

/** Shows which flags still use the segment before it goes; deleting an in-use one takes an explicit tick. */
export function DeleteSegmentDialog({ workspaceId, segment, onClose, onDeleted }: Props) {
  const [usage, setUsage] = useState<SegmentUsage | null>(null)
  const [usageError, setUsageError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const [understood, setUnderstood] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const slow = useSlowHint(busy)

  useEffect(() => {
    let cancelled = false
    setUsage(null)
    setUsageError(null)
    segmentUsage(workspaceId, segment.key)
      .then((u) => {
        if (!cancelled) setUsage(u)
      })
      .catch((err) => {
        if (!cancelled) setUsageError(describeError(err, 'see where this segment is used'))
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId, segment.key, attempt])

  const used = usage !== null && usage.flags.length > 0

  async function confirm() {
    if (usage === null) return
    if (used && !understood) {
      setError('Tick the box to confirm you want to delete a segment that flags still use.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await deleteSegment(workspaceId, segment.key, used)
      onDeleted()
      onClose()
    } catch (err) {
      setError(describeError(err, 'delete this segment'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <ConfirmDialog
      title={`Delete segment ${segment.name}?`}
      confirmLabel={used ? 'Delete anyway' : 'Delete segment'}
      tone="danger"
      busy={busy}
      error={error}
      slowHint={busy && slow ? SLOW_HINT : null}
      onConfirm={() => void confirm()}
      onCancel={onClose}
    >
      <p>
        <span className="font-mono">{segment.key}</span> and its {segment.memberCount.toLocaleString('en-US')} explicit member
        {segment.memberCount === 1 ? '' : 's'} will be deleted. This cannot be undone.
      </p>
      {usage === null && !usageError && (
        <p role="status" className="text-zinc-400">
          Checking which flags use it…
        </p>
      )}
      {usageError && (
        <p role="alert" className="text-red-300">
          {usageError}{' '}
          <button className="underline" onClick={() => setAttempt((n) => n + 1)}>
            Retry
          </button>
        </p>
      )}
      {usage && !used && <p className="text-zinc-400">No flag rules use this segment.</p>}
      {used && usage && (
        <div role="alert" className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-amber-100">
          <p className="font-medium">
            Warning: {usage.flags.length} flag{usage.flags.length === 1 ? '' : 's'} still use{usage.flags.length === 1 ? 's' : ''} this segment.
          </p>
          <ul className="list-disc pl-5" aria-label="Flags using this segment">
            {usage.flags.map((f) => (
              <li key={f.flagKey}>
                <span className="font-mono">{f.flagKey}</span> in {f.environments.map((e) => e.key).join(', ')}
              </li>
            ))}
          </ul>
          <p>
            Those rules will stay in place but match nobody, including &ldquo;is not in segment&rdquo; rules, until you edit them.
          </p>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={understood} disabled={busy} onChange={(e) => setUnderstood(e.target.checked)} />
            <span>I understand, delete it anyway</span>
          </label>
        </div>
      )}
    </ConfirmDialog>
  )
}

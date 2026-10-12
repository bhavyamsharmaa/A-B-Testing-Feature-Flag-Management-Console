import { useEffect, useMemo, useState } from 'react'
import { ApiError } from '../../api/client'
import { createSegment, getSegment, updateSegment } from '../../api/segments'
import { describeError } from '../../lib/errors'
import { MAX_MEMBERS } from '../../lib/segmentMembers'
import {
  clausesFromSaved,
  clausesToPayload,
  newClause,
  parseServerSegmentRuleError,
  validateClause,
  type DraftClause,
} from '../../lib/targeting'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import type { Clause, Segment } from '../../types'
import { ConfirmDialog } from '../ConfirmDialog'
import { Modal } from '../Modal'
import { ClauseRow, field } from '../TargetingEditor'
import { MemberEditor } from './MemberEditor'

const KEY_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/

interface Props {
  workspaceId: string
  /** Null creates a segment; a key edits that one. */
  segmentKey: string | null
  readOnly: boolean
  onClose: () => void
  /** The server's version after a successful save. */
  onSaved: (segment: Segment) => void
}

const sameSet = (a: string[], b: string[]) => {
  if (a.length !== b.length) return false
  const s = new Set(a)
  return b.every((x) => s.has(x))
}

export function SegmentEditor({ workspaceId, segmentKey, readOnly, onClose, onSaved }: Props) {
  const creating = segmentKey === null
  const [loaded, setLoaded] = useState<Segment | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loadAttempt, setLoadAttempt] = useState(0)

  const [key, setKey] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [clauses, setClauses] = useState<DraftClause[]>([])
  /** Rules the builder can't draw; kept and re-sent unchanged unless the person replaces them. */
  const [lockedRules, setLockedRules] = useState<Clause[] | null>(null)
  const [members, setMembers] = useState<string[]>([])

  const [showErrors, setShowErrors] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [keyTaken, setKeyTaken] = useState(false)
  const [stale, setStale] = useState(false)
  const [serverClause, setServerClause] = useState<{ clause: number | null; text: string } | null>(null)
  const [discarding, setDiscarding] = useState(false)
  const slow = useSlowHint(saving)

  function adopt(seg: Segment) {
    setLoaded(seg)
    setKey(seg.key)
    setName(seg.name)
    setDescription(seg.description)
    const drafts = clausesFromSaved(seg.rules)
    setClauses(drafts ?? [])
    setLockedRules(drafts ? null : seg.rules)
    setMembers(seg.members)
  }

  useEffect(() => {
    if (creating) return
    let cancelled = false
    setLoaded(null)
    setLoadError(null)
    getSegment(workspaceId, segmentKey)
      .then((seg) => {
        if (!cancelled) adopt(seg)
      })
      .catch((err) => {
        if (!cancelled) setLoadError(describeError(err, 'view this segment'))
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, segmentKey, loadAttempt])

  const rulesPayload = useMemo(() => lockedRules ?? clausesToPayload(clauses), [lockedRules, clauses])
  const rulesJSON = JSON.stringify(rulesPayload)
  const dirty = creating
    ? key !== '' || name !== '' || description !== '' || clauses.length > 0 || members.length > 0
    : loaded !== null &&
      (name !== loaded.name || description !== loaded.description || rulesJSON !== JSON.stringify(loaded.rules) || !sameSet(members, loaded.members))

  const clauseErrors = clauses.map((c) => validateClause(c, null))
  const problems = [
    creating && !KEY_RE.test(key) ? 'Key: 1-128 letters, digits, "_", "." or "-", starting with a letter or digit.' : null,
    name.trim() === '' ? 'Name is required.' : null,
    name.length > 120 ? 'Name is at most 120 characters.' : null,
    description.length > 1000 ? 'Description is at most 1,000 characters.' : null,
    clauseErrors.some((e) => e !== null) ? 'Fix the rules marked below.' : null,
    members.length > MAX_MEMBERS ? `At most ${MAX_MEMBERS.toLocaleString('en-US')} members.` : null,
  ].filter((p): p is string => p !== null)

  function requestClose() {
    if (saving) return
    if (dirty && !readOnly) setDiscarding(true)
    else onClose()
  }

  async function save() {
    setShowErrors(true)
    setError(null)
    setKeyTaken(false)
    setStale(false)
    setServerClause(null)
    if (problems.length > 0) return
    setSaving(true)
    try {
      let saved: Segment
      if (creating) {
        saved = await createSegment(workspaceId, { key, name: name.trim(), description, rules: rulesPayload, members })
      } else if (loaded) {
        saved = await updateSegment(workspaceId, loaded.key, {
          ...(name !== loaded.name ? { name: name.trim() } : {}),
          ...(description !== loaded.description ? { description } : {}),
          ...(rulesJSON !== JSON.stringify(loaded.rules) ? { rules: rulesPayload } : {}),
          ...(!sameSet(members, loaded.members) ? { members } : {}),
          expectedVersion: loaded.version,
        })
      } else {
        return
      }
      onSaved(saved)
      onClose()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'SEGMENT_KEY_EXISTS') setKeyTaken(true)
      else if (err instanceof ApiError && err.code === 'STALE_SEGMENT') setStale(true)
      else if (err instanceof ApiError && err.status === 400 && parseServerSegmentRuleError(err.message)) setServerClause(parseServerSegmentRuleError(err.message))
      else setError(describeError(err, creating ? 'create segments' : 'change this segment'))
    } finally {
      setSaving(false)
    }
  }

  const title = creating ? 'New segment' : readOnly ? `Segment ${segmentKey}` : `Edit segment ${segmentKey}`
  const disabled = readOnly || saving
  const loadingExisting = !creating && loaded === null && !loadError

  return (
    <>
      <Modal title={title} onClose={requestClose} busy={saving} wide>
        {loadError && (
          <div role="alert" className="mt-4 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            <p>{loadError}</p>
            <button onClick={() => setLoadAttempt((n) => n + 1)} className="mt-2 underline">
              Retry
            </button>
          </div>
        )}
        {loadingExisting && (
          <div className="mt-4 space-y-2" role="status" aria-label="Loading segment">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-10 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
            ))}
          </div>
        )}

        {(creating || loaded) && (
          <div className="mt-4 space-y-5">
            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <label htmlFor="seg-key" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
                  Key
                </label>
                <input
                  id="seg-key"
                  className={`${field} mt-1 w-full font-mono`}
                  value={key}
                  disabled={!creating || saving}
                  spellCheck={false}
                  autoComplete="off"
                  autoFocus={creating}
                  placeholder="beta-testers"
                  aria-describedby="seg-key-help"
                  onChange={(e) => {
                    setKey(e.target.value)
                    setKeyTaken(false)
                  }}
                />
                <p id="seg-key-help" className="mt-1 text-xs text-zinc-500">
                  {creating ? 'Flag rules refer to the segment by this key. It cannot be changed later.' : 'The key cannot be changed.'}
                </p>
                {showErrors && creating && !KEY_RE.test(key) && <p className="mt-1 text-xs text-red-400">Use 1-128 letters, digits, &ldquo;_&rdquo;, &ldquo;.&rdquo; or &ldquo;-&rdquo;, starting with a letter or digit.</p>}
                {keyTaken && (
                  <p role="alert" className="mt-1 text-xs text-red-400">
                    A segment with this key already exists in this workspace.
                  </p>
                )}
              </div>
              <div>
                <label htmlFor="seg-name" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
                  Name
                </label>
                <input id="seg-name" className={`${field} mt-1 w-full`} value={name} disabled={disabled} maxLength={120} placeholder="Beta testers" onChange={(e) => setName(e.target.value)} />
                {showErrors && name.trim() === '' && <p className="mt-1 text-xs text-red-400">Name is required.</p>}
              </div>
            </div>
            <div>
              <label htmlFor="seg-desc" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
                Description
              </label>
              <textarea id="seg-desc" className={`${field} mt-1 h-16 w-full resize-y`} value={description} disabled={disabled} maxLength={1000} onChange={(e) => setDescription(e.target.value)} />
            </div>

            <section aria-labelledby="seg-rules-h">
              <h3 id="seg-rules-h" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
                Rules
              </h3>
              <p className="mt-1 text-xs text-zinc-400">
                Someone is in this segment if they are an explicit member below, <strong className="font-semibold text-zinc-200">or</strong> they
                match <strong className="font-semibold text-zinc-200">all</strong> of these rules. With no rules, only the members are in.
              </p>
              <div className="mt-3 space-y-2">
                {lockedRules ? (
                  <div className="space-y-2">
                    <p className="text-xs text-amber-300">These rules use a shape the builder can&rsquo;t edit. They are kept exactly as they are when you save, unless you replace them.</p>
                    <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-white/10 bg-black/40 p-2 font-mono text-xs text-zinc-300">{JSON.stringify(lockedRules, null, 2)}</pre>
                    {!disabled && (
                      <button className="text-xs text-accent underline" onClick={() => { setLockedRules(null); setClauses([newClause()]) }}>
                        Replace with new rules
                      </button>
                    )}
                  </div>
                ) : (
                  <>
                    {clauses.length === 0 && (
                      <p className="rounded-lg border border-dashed border-white/10 px-4 py-4 text-center text-sm text-zinc-400">No rules: only the explicit members are in this segment.</p>
                    )}
                    {clauses.map((c, j) => (
                      <div key={c.id}>
                        {j > 0 && <div className="py-1 text-[11px] font-semibold tracking-widest text-accent">AND</div>}
                        <ClauseRow
                          clause={c}
                          disabled={disabled}
                          canRemove
                          segments={null}
                          noSegmentOperators
                          error={showErrors ? clauseErrors[j] : null}
                          serverText={serverClause && serverClause.clause === j ? serverClause.text : null}
                          onChange={(next) => {
                            setClauses(clauses.map((x, k) => (k === j ? next : x)))
                            setServerClause(null)
                          }}
                          onRemove={() => setClauses(clauses.filter((_, k) => k !== j))}
                        />
                      </div>
                    ))}
                    {!disabled && (
                      <button className="text-xs text-accent transition hover:underline" onClick={() => setClauses([...clauses, newClause()])}>
                        + Add rule (AND)
                      </button>
                    )}
                  </>
                )}
              </div>
            </section>

            <section aria-labelledby="seg-members-h">
              <h3 id="seg-members-h" className="mb-2 text-xs font-medium uppercase tracking-wider text-zinc-500">
                Explicit members
              </h3>
              <MemberEditor members={members} disabled={disabled} onChange={setMembers} />
            </section>

            {stale && (
              <div role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
                <p>Someone else changed this segment while you were editing. Your changes were not saved.</p>
                <button
                  className="mt-2 underline"
                  onClick={() => {
                    setStale(false)
                    setLoadAttempt((n) => n + 1) // reload the latest; your edits in this form are replaced
                  }}
                >
                  Load the latest version (discards your edits)
                </button>
              </div>
            )}
            {error && (
              <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                {error}
              </p>
            )}
            {serverClause && serverClause.clause === null && (
              <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                {serverClause.text}
              </p>
            )}
            {showErrors && problems.length > 0 && (
              <ul className="list-disc pl-5 text-xs text-red-400" aria-label="Problems to fix">
                {problems.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            )}
            {slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}

            <div className="flex justify-end gap-2 border-t border-white/5 pt-4">
              <button onClick={requestClose} disabled={saving} className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50">
                {readOnly ? 'Close' : 'Cancel'}
              </button>
              {!readOnly && (
                <button
                  onClick={() => void save()}
                  disabled={saving || (!creating && !dirty)}
                  className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {saving ? 'Saving…' : creating ? 'Create segment' : 'Save changes'}
                </button>
              )}
            </div>
          </div>
        )}
      </Modal>
      {discarding && (
        <ConfirmDialog
          title="Discard your changes?"
          confirmLabel="Discard"
          tone="danger"
          busy={false}
          error={null}
          slowHint={null}
          onConfirm={onClose}
          onCancel={() => setDiscarding(false)}
        >
          <p>You have unsaved changes to this segment.</p>
        </ConfirmDialog>
      )}
    </>
  )
}

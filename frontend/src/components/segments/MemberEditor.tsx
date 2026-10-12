import { useDeferredValue, useMemo, useState } from 'react'
import {
  MAX_KEY_LENGTH,
  MAX_MEMBERS,
  MAX_PASTE_CHARS,
  describeMerge,
  filterMembers,
  mergeMembers,
  parseMemberText,
} from '../../lib/segmentMembers'
import { field, iconBtn } from '../TargetingEditor'

/** How many members are drawn at once; the filter finds the rest. */
const RENDER_LIMIT = 100

interface Props {
  members: string[]
  disabled: boolean
  onChange: (members: string[]) => void
}

/**
 * The explicit members of a segment. Built for pasting a long list: the text is
 * parsed and checked before anything is added (de-duplicated, length- and
 * limit-checked, all or nothing), and only a screenful of members is drawn, so
 * ten thousand keys stay fast.
 */
export function MemberEditor({ members, disabled, onChange }: Props) {
  const [text, setText] = useState('')
  const [status, setStatus] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)
  const [filter, setFilter] = useState('')
  const [confirmClear, setConfirmClear] = useState(false)

  // Parsing a big paste must not make typing in the box stutter.
  const deferred = useDeferredValue(text)
  const preview = useMemo(() => (deferred.trim() === '' ? null : parseMemberText(deferred)), [deferred])
  const { shown, matches } = useMemo(() => filterMembers(members, filter, RENDER_LIMIT), [members, filter])

  function add() {
    const parsed = parseMemberText(text)
    const merged = mergeMembers(members, parsed, MAX_MEMBERS)
    if (merged.error) {
      setStatus({ kind: 'error', text: merged.error })
      return // the text stays, so nothing the person pasted is lost
    }
    onChange(merged.members)
    setStatus({ kind: 'ok', text: describeMerge(merged, parsed) })
    setText('')
  }

  const remove = (key: string) => {
    onChange(members.filter((m) => m !== key))
    setStatus(null)
  }

  const full = members.length >= MAX_MEMBERS

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <p className="text-sm text-zinc-300" data-testid="member-count">
          <span className="font-semibold">{members.length.toLocaleString('en-US')}</span> of {MAX_MEMBERS.toLocaleString('en-US')} members
          {full && <span className="ml-2 text-amber-300">Limit reached</span>}
        </p>
        {members.length > 0 &&
          !disabled &&
          (confirmClear ? (
            <span className="flex items-center gap-2 text-xs">
              <span className="text-zinc-300">Remove all {members.length.toLocaleString('en-US')} members?</span>
              <button
                className={`${iconBtn} hover:!border-red-500/60 hover:!text-red-300`}
                onClick={() => {
                  onChange([])
                  setConfirmClear(false)
                  setStatus(null)
                }}
              >
                Yes, remove all
              </button>
              <button className={iconBtn} onClick={() => setConfirmClear(false)}>
                Keep
              </button>
            </span>
          ) : (
            <button className={iconBtn} onClick={() => setConfirmClear(true)}>
              Remove all
            </button>
          ))}
      </div>

      {!disabled && (
        <div>
          <label htmlFor="member-paste" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Add member keys
          </label>
          <textarea
            id="member-paste"
            className={`${field} mt-1 h-24 w-full resize-y font-mono`}
            placeholder={'user-1\nuser-2\nuser-3'}
            spellCheck={false}
            autoComplete="off"
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setStatus(null)
            }}
            aria-describedby="member-paste-help"
          />
          <p id="member-paste-help" className="mt-1 text-xs text-zinc-500">
            One key per line, or separated by commas or tabs, so a spreadsheet column pastes as it is. Keys are exact and case-sensitive,
            at most {MAX_KEY_LENGTH} characters. Repeats are ignored.
          </p>
          {text.length > MAX_PASTE_CHARS ? (
            <p role="alert" className="mt-1 text-xs text-red-400">
              That is too much to paste at once. Add the keys in smaller batches.
            </p>
          ) : (
            preview && (
              <p className="mt-1 text-xs text-zinc-400" aria-live="polite">
                {preview.keys.length.toLocaleString('en-US')} key{preview.keys.length === 1 ? '' : 's'} found
                {preview.duplicates > 0 && `, ${preview.duplicates.toLocaleString('en-US')} repeated`}
                {preview.invalidCount > 0 && (
                  <span className="text-red-400">
                    , {preview.invalidCount.toLocaleString('en-US')} not usable (first: &ldquo;{preview.invalid[0].value}&rdquo; is {preview.invalid[0].reason})
                  </span>
                )}
                .
              </p>
            )
          )}
          <div className="mt-2 flex items-center gap-3">
            <button
              onClick={add}
              disabled={text.trim() === '' || text.length > MAX_PASTE_CHARS}
              className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40"
            >
              Add keys
            </button>
            {status && (
              <p role={status.kind === 'error' ? 'alert' : 'status'} className={`text-xs ${status.kind === 'error' ? 'text-red-400' : 'text-emerald-300'}`}>
                {status.kind === 'error' ? 'Not added. ' : ''}
                {status.text}
              </p>
            )}
          </div>
        </div>
      )}

      {members.length === 0 ? (
        <p className="rounded-lg border border-dashed border-white/10 px-4 py-5 text-center text-sm text-zinc-400">
          No explicit members. {disabled ? '' : 'Paste keys above, or rely on the rules alone.'}
        </p>
      ) : (
        <div className="space-y-2">
          <div>
            <label htmlFor="member-filter" className="sr-only">
              Filter members
            </label>
            <input
              id="member-filter"
              className={`${field} w-full sm:w-72`}
              placeholder="Filter members…"
              spellCheck={false}
              autoComplete="off"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </div>
          {matches === 0 ? (
            <p className="text-sm text-zinc-400">No member matches &ldquo;{filter}&rdquo;.</p>
          ) : (
            <ul className="flex max-h-56 flex-wrap gap-1.5 overflow-y-auto rounded-lg border border-white/10 bg-black/30 p-2" aria-label="Members">
              {shown.map((m) => (
                <li key={m} className="inline-flex max-w-full items-center gap-1 rounded-full bg-accent/15 px-2 py-0.5 font-mono text-xs text-accent">
                  <span className="truncate">{m}</span>
                  {!disabled && (
                    <button onClick={() => remove(m)} aria-label={`Remove member ${m}`} className="text-accent/70 transition hover:text-white">
                      ✕
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {matches > shown.length && (
            <p className="text-xs text-zinc-500">
              Showing {shown.length} of {matches.toLocaleString('en-US')}
              {filter.trim() !== '' ? ' matches' : ' members'}. Filter to find a specific key.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

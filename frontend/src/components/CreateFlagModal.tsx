import { useEffect, useState, type FormEvent } from 'react'
import { createBooleanFlag, createFlag } from '../api/flags'
import { ApiError } from '../api/client'
import { describeError } from '../lib/errors'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import {
  defaultRows,
  mapServerError,
  rowsForType,
  toVariations,
  validateRows,
  type DraftVariation,
  type NonBooleanType,
  type ServerRowError,
  type Validation,
  type VariationType,
} from '../lib/variations'
import { VariationsEditor } from './VariationsEditor'

// Same rule the backend enforces (flags/validate.go).
const KEY_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/
const KEY_RULE = "1–128 characters: letters, digits, '_', '.', '-', starting with a letter or digit."

interface Props {
  env: string
  onClose: () => void
  onCreated: (key: string) => void
}

export function CreateFlagModal({ env, onClose, onCreated }: Props) {
  const [key, setKey] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [keyError, setKeyError] = useState<string | null>(null)
  const [nameError, setNameError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [type, setType] = useState<VariationType>('boolean')
  const [rows, setRows] = useState<DraftVariation[]>([])
  const [validation, setValidation] = useState<Validation | null>(null)
  const [serverRowError, setServerRowError] = useState<ServerRowError | null>(null)
  const slow = useSlowHint(submitting)

  function onTypeChange(next: VariationType) {
    setValidation(null)
    setServerRowError(null)
    setFormError(null)
    setType(next)
    // Ids carry over between non-boolean types; values reset, since "5" the text is not 5 the number.
    setRows((prev) => (next === 'boolean' ? [] : prev.length > 0 ? rowsForType(next, prev) : defaultRows(next)))
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !submitting) onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [submitting, onClose])

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    const k = key.trim()
    const kErr = KEY_RE.test(k) ? null : KEY_RULE
    const nErr = name.trim() ? null : 'Name is required.'
    setKeyError(kErr)
    setNameError(nErr)
    setFormError(null)
    setServerRowError(null)
    const checked = type === 'boolean' ? null : validateRows(type, rows)
    setValidation(checked)
    if (kErr || nErr || (checked && !checked.ok)) return

    setSubmitting(true)
    try {
      if (type === 'boolean') {
        await createBooleanFlag(env, { key: k, name, description })
      } else {
        await createFlag(env, { key: k, name, description, variationType: type, variations: toVariations(type, rows) })
      }
      onCreated(k)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.code === 'FLAG_KEY_EXISTS') {
        setKeyError(`A flag with the key "${k}" already exists. Choose a different key.`)
      } else if (err instanceof ApiError && err.status === 400 && type !== 'boolean') {
        // TYPE_MISMATCH and INVALID_REQUEST: show it on the variation it names, else on the form.
        const onRow = mapServerError(err.message, rows)
        if (onRow) setServerRowError(onRow)
        else setFormError(describeError(err, 'create flags'))
      } else {
        setFormError(describeError(err, 'create flags'))
      }
      setSubmitting(false)
    }
  }

  const input =
    'mt-1 w-full rounded-lg border bg-black/40 px-3 py-2.5 text-sm outline-none transition focus:ring-4 disabled:opacity-60'
  const ok = 'border-white/10 focus:border-accent focus:ring-accent/20'
  const bad = 'border-red-500/60 focus:border-red-500 focus:ring-red-500/20'

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 px-4 backdrop-blur-sm"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && !submitting) onClose()
      }}
    >
      <form
        onSubmit={onSubmit}
        noValidate
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-title"
        className={`w-full animate-fade-up rounded-2xl border border-white/10 bg-surface p-6 ${
          type === 'boolean' ? 'max-w-md' : 'max-h-[90vh] max-w-2xl overflow-y-auto'
        }`}
      >
        <h2 id="create-title" className="text-lg font-semibold">
          Create flag
        </h2>
        <p className="mt-1 text-sm text-zinc-400">
          Creates a {type} flag in <span className="font-medium text-zinc-200">every environment of this workspace</span>, disabled
          everywhere. Turning it on is a separate step.
        </p>

        <label htmlFor="flag-key" className="mt-5 block text-sm text-zinc-300">
          Key
        </label>
        <input
          id="flag-key"
          autoFocus
          value={key}
          onChange={(e) => setKey(e.target.value)}
          disabled={submitting}
          placeholder="new-checkout"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={keyError !== null}
          className={`${input} font-mono ${keyError ? bad : ok}`}
        />
        {keyError ? (
          <p role="alert" className="mt-1 text-xs text-red-400">
            {keyError}
          </p>
        ) : (
          <p className="mt-1 text-xs text-zinc-500">{KEY_RULE}</p>
        )}

        <label htmlFor="flag-name" className="mt-4 block text-sm text-zinc-300">
          Name
        </label>
        <input
          id="flag-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={submitting}
          placeholder="New checkout flow"
          aria-invalid={nameError !== null}
          className={`${input} ${nameError ? bad : ok}`}
        />
        {nameError && (
          <p role="alert" className="mt-1 text-xs text-red-400">
            {nameError}
          </p>
        )}

        <label htmlFor="flag-desc" className="mt-4 block text-sm text-zinc-300">
          Description <span className="text-zinc-500">(optional)</span>
        </label>
        <textarea
          id="flag-desc"
          rows={3}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          disabled={submitting}
          className={`${input} ${ok}`}
        />

        <label htmlFor="flag-type" className="mt-4 block text-sm text-zinc-300">
          Type
        </label>
        <select
          id="flag-type"
          value={type}
          onChange={(e) => onTypeChange(e.target.value as VariationType)}
          disabled={submitting}
          className={`${input} ${ok}`}
        >
          <option value="boolean">Boolean (on / off)</option>
          <option value="string">String</option>
          <option value="number">Number</option>
          <option value="json">JSON</option>
        </select>

        {type !== 'boolean' && (
          <div className="mt-4">
            <VariationsEditor
              type={type as NonBooleanType}
              rows={rows}
              validation={validation}
              serverError={serverRowError}
              disabled={submitting}
              onChange={(next) => {
                setRows(next)
                setServerRowError(null)
              }}
            />
          </div>
        )}

        {slow && <p className="mt-3 text-sm text-amber-300">{SLOW_HINT}</p>}
        {formError && (
          <p role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {formError}
          </p>
        )}

        <div className="mt-6 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={submitting}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {submitting ? 'Creating…' : 'Create flag'}
          </button>
        </div>
      </form>
    </div>
  )
}

import { useState } from 'react'
import type { ApiToken } from '../../types'
import { Modal } from '../Modal'
import { CopyField } from '../settings/CopyField'

interface Props {
  token: ApiToken
  plaintext: string
  onDone: () => void
}

/**
 * The token, shown ONCE. It is not stored anywhere that can show it again, so this dialog cannot be dismissed by
 * Escape or a stray click: only "I have stored it" closes it.
 */
export function ShowTokenDialog({ token, plaintext, onDone }: Props) {
  const [stored, setStored] = useState(false)
  return (
    // busy: nothing but the button below closes it
    <Modal title="Copy your new token" onClose={() => undefined} busy wide>
      <div className="mt-4 space-y-4">
        <div role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-sm text-amber-100">
          <p className="font-semibold">This is the only time the token is shown.</p>
          <p className="mt-1">
            Helios keeps only a hash of it and cannot show it again. Copy it now into your CI system&rsquo;s secret store. Anyone who has it can act
            as <span className="font-mono">{token.name}</span> ({token.role}) until it expires or is revoked.
          </p>
        </div>
        <CopyField value={plaintext} label="New API token" />
        <p className="text-xs text-zinc-400">
          Use it as <span className="font-mono">Authorization: Bearer &lt;token&gt;</span>, from an environment variable, never in a URL. It begins with{' '}
          <span className="font-mono">{token.prefix}</span>, which is what the list and the audit log show.
        </p>
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-0.5" checked={stored} onChange={(e) => setStored(e.target.checked)} />
          <span>I have copied the token and stored it safely</span>
        </label>
        <div className="flex justify-end">
          <button
            onClick={onDone}
            disabled={!stored}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Done
          </button>
        </div>
      </div>
    </Modal>
  )
}

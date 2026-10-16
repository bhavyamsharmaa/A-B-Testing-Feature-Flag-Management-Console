import { useState } from 'react'
import { Modal } from '../Modal'
import { CopyField } from '../settings/CopyField'

interface Props {
  url: string
  secret: string
  rotated: boolean
  onDone: () => void
}

/** The signing secret, shown ONCE. Only the button closes it: Escape and a stray click do not. */
export function ShowSecretDialog({ url, secret, rotated, onDone }: Props) {
  const [stored, setStored] = useState(false)
  return (
    <Modal title={rotated ? 'Your new signing secret' : 'Copy your signing secret'} onClose={() => undefined} busy wide>
      <div className="mt-4 space-y-4">
        <div role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-sm text-amber-100">
          <p className="font-semibold">This is the only time the secret is shown.</p>
          <p className="mt-1">
            Helios stores it encrypted and cannot show it again. Put it where <span className="font-mono">{url}</span> can read it, to verify the
            signature of each delivery.
            {rotated && ' The previous secret keeps signing for 24 hours (each delivery carries both signatures), so you can switch without missing events.'}
          </p>
        </div>
        <CopyField value={secret} label="Signing secret" />
        <p className="text-xs text-zinc-400">
          Each request carries <span className="font-mono">Helios-Signature</span>, <span className="font-mono">Helios-Timestamp</span> and{' '}
          <span className="font-mono">Helios-Delivery-Id</span>. Sign <span className="font-mono">timestamp.deliveryId.body</span> with HMAC-SHA256 and
          reject timestamps older than five minutes; see docs/WEBHOOKS.md for code. Deliveries are at least once: use the delivery id to ignore repeats.
        </p>
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-0.5" checked={stored} onChange={(e) => setStored(e.target.checked)} />
          <span>I have copied the secret and stored it safely</span>
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

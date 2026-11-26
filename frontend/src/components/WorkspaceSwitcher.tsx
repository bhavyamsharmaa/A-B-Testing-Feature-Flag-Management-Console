import { useEffect, useRef, useState } from 'react'
import { ROLE_LABEL } from '../lib/permissions'
import { useWorkspace } from '../workspace/WorkspaceProvider'
import { CreateWorkspaceModal } from './CreateWorkspaceModal'

/** The current workspace's name, a dropdown of the user's workspaces, and "Create workspace". */
export function WorkspaceSwitcher() {
  const { workspaces, active, activate } = useWorkspace()
  const [open, setOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [switchingTo, setSwitchingTo] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const root = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (!active) return null

  async function choose(id: string) {
    if (id === active?.id) {
      setOpen(false)
      return
    }
    setSwitchingTo(id)
    setError(null)
    try {
      await activate(id)
      setOpen(false)
    } catch {
      setError('Could not switch workspace. Try again.')
    } finally {
      setSwitchingTo(null)
    }
  }

  return (
    <div ref={root} className="relative" data-testid="workspace-switcher">
      <button
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Workspace: ${active.name}. Switch workspace`}
        className="flex max-w-[16rem] items-center gap-2 rounded-lg border border-white/10 bg-white/[0.03] px-3 py-1.5 text-sm text-zinc-200 backdrop-blur transition hover:border-accent/60 hover:bg-accent/10"
      >
        <span className="truncate font-medium" data-testid="workspace-name">
          {active.name}
        </span>
        <span className="rounded bg-white/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-zinc-400">{ROLE_LABEL[active.role]}</span>
        <svg viewBox="0 0 20 20" className={`h-4 w-4 shrink-0 text-zinc-500 transition ${open ? 'rotate-180' : ''}`} fill="currentColor" aria-hidden>
          <path d="M5.3 7.3a1 1 0 0 1 1.4 0L10 10.6l3.3-3.3a1 1 0 1 1 1.4 1.4l-4 4a1 1 0 0 1-1.4 0l-4-4a1 1 0 0 1 0-1.4Z" />
        </svg>
      </button>

      {open && (
        <div
          role="menu"
          aria-label="Your workspaces"
          className="absolute left-0 z-40 mt-2 w-72 animate-fade-up overflow-hidden rounded-xl border border-white/10 bg-surface shadow-[0_20px_60px_-20px_rgba(0,0,0,0.9)]"
        >
          <p className="px-3.5 pb-1 pt-3 text-[11px] font-medium uppercase tracking-wider text-zinc-500">Your workspaces</p>
          <ul className="max-h-72 overflow-y-auto py-1">
            {workspaces.map((w) => {
              const isActive = w.id === active.id
              return (
                <li key={w.id}>
                  <button
                    role="menuitemradio"
                    aria-checked={isActive}
                    onClick={() => void choose(w.id)}
                    disabled={switchingTo !== null}
                    className={`flex w-full items-center justify-between gap-3 px-3.5 py-2 text-left text-sm transition hover:bg-white/5 disabled:opacity-60 ${
                      isActive ? 'text-white' : 'text-zinc-300'
                    }`}
                  >
                    <span className="min-w-0">
                      <span className="block truncate">{w.name}</span>
                      <span className="block text-[11px] text-zinc-500">
                        {ROLE_LABEL[w.role]} · {w.environments.length} environments
                      </span>
                    </span>
                    {switchingTo === w.id ? (
                      <span className="text-xs text-zinc-400">Switching…</span>
                    ) : isActive ? (
                      <svg viewBox="0 0 20 20" className="h-4 w-4 shrink-0 text-accent" fill="currentColor" aria-hidden>
                        <path d="M16.7 5.3a1 1 0 0 1 0 1.4l-7.5 7.5a1 1 0 0 1-1.4 0L3.3 9.7a1 1 0 1 1 1.4-1.4l3.8 3.8 6.8-6.8a1 1 0 0 1 1.4 0Z" />
                      </svg>
                    ) : null}
                  </button>
                </li>
              )
            })}
          </ul>
          {error && <p role="alert" className="px-3.5 pb-2 text-xs text-red-300">{error}</p>}
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false)
              setCreating(true)
            }}
            className="flex w-full items-center gap-2 border-t border-white/10 px-3.5 py-2.5 text-left text-sm text-accent transition hover:bg-accent/10"
          >
            <span aria-hidden className="text-base leading-none">+</span> Create workspace
          </button>
        </div>
      )}

      {creating && <CreateWorkspaceModal onClose={() => setCreating(false)} onCreated={() => setCreating(false)} />}
    </div>
  )
}

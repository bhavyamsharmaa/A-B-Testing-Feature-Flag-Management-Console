import { NavLink } from 'react-router-dom'
import { Logo } from './Logo'
import { WorkspaceSwitcher } from './WorkspaceSwitcher'

const tab = ({ isActive }: { isActive: boolean }) =>
  `rounded-lg px-3.5 py-1.5 text-sm transition ${
    isActive ? 'bg-accent/20 text-white shadow-[0_0_20px_-6px_rgba(124,92,255,0.7)]' : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
  }`

export function ConsoleHeader({ email, onSignOut }: { email: string; onSignOut: () => void }) {
  return (
    <>
      <header className="flex animate-fade-up flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-4">
          <h1>
            <Logo className="text-xl" />
          </h1>
          <WorkspaceSwitcher />
        </div>
        <div className="flex items-center gap-3">
          <span className="hidden text-sm text-zinc-400 sm:inline">{email}</span>
          <button
            onClick={onSignOut}
            className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 backdrop-blur transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
          >
            Sign out
          </button>
        </div>
      </header>
      <nav aria-label="Console sections" className="mt-6 flex gap-1">
        <NavLink to="/console" end className={tab}>
          Flags
        </NavLink>
        <NavLink to="/console/audit" className={tab}>
          Audit log
        </NavLink>
        <NavLink to="/console/settings" className={tab}>
          Settings
        </NavLink>
      </nav>
    </>
  )
}

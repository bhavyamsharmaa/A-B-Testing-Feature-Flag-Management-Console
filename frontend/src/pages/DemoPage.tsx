import type { ReactNode } from 'react'
import { ConfigError } from '../components/ConfigError'
import { Logo } from '../components/Logo'
import { heliosSdkKey } from '../config'
import { useFlag, type StreamStatus } from '../demo/useFlag'

const FLAG_KEY = 'demo-banner'

// Shown in the "How this page is wired" panel. Keep in sync with DemoContent below.
const SNIPPET = `const { on: showBanner } = useFlag('demo-banner')
// on === true only if POST /evaluate says so; false if Helios is unreachable

<Reveal show={showBanner}>
  <PromoBanner />
</Reveal>`

export default function DemoPage() {
  if (!heliosSdkKey) return <ConfigError missing={['VITE_HELIOS_SDK_KEY']} />
  return <DemoContent />
}

/** Animates children in and out. The feature code stays a plain `show` check. */
function Reveal({ show, children }: { show: boolean; children: ReactNode }) {
  return (
    <div
      aria-hidden={!show}
      className={`grid transition-all duration-500 ease-out ${
        show ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0'
      }`}
    >
      <div className="overflow-hidden">{children}</div>
    </div>
  )
}

function PromoBanner() {
  return (
    <div className="bg-gradient-to-r from-accent via-indigo-500 to-fuchsia-500 px-4 py-3 text-center text-sm font-medium text-white shadow-[0_8px_40px_-8px_rgba(124,92,255,0.9)]">
      <span className="mr-2 rounded-full bg-white/20 px-2 py-0.5 text-xs font-semibold tracking-wide">NEW</span>
      Launch week: 20% off every annual plan. Ends Sunday.
    </div>
  )
}

function StatusChip({ status, lastUpdate }: { status: StreamStatus; lastUpdate: Date | null }) {
  const live = status === 'live'
  return (
    <div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-1 text-xs text-zinc-400">
      <span
        className={`inline-flex items-center gap-2 rounded-full border px-3 py-1 ${
          live ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300' : 'border-amber-500/40 bg-amber-500/10 text-amber-300'
        }`}
      >
        <span className={`h-2 w-2 rounded-full ${live ? 'animate-pulse bg-emerald-400' : 'bg-amber-400'}`} />
        {live ? 'Live' : status === 'connecting' ? 'Connecting' : 'Reconnecting'}
      </span>
      <span>Last update: {lastUpdate ? lastUpdate.toLocaleTimeString() : 'no changes yet'}</span>
    </div>
  )
}

function DemoContent() {
  const { on: showBanner, status, lastUpdate } = useFlag(FLAG_KEY)

  return (
    <div className="min-h-screen">
      <Reveal show={showBanner}>
        <PromoBanner />
      </Reveal>

      <header className="mx-auto flex max-w-4xl items-center justify-between px-4 py-5">
        <Logo />
        <nav className="hidden gap-6 text-sm text-zinc-400 sm:flex">
          <span>Product</span>
          <span>Pricing</span>
          <span>Docs</span>
        </nav>
      </header>

      <main className="mx-auto max-w-4xl space-y-12 px-4 pb-16">
        <section className="animate-fade-up pt-8 text-center">
          <h1 className="text-4xl font-semibold tracking-tight sm:text-5xl">Ship features without shipping risk</h1>
          <p className="mx-auto mt-4 max-w-xl text-zinc-400">
            Turn features on for a few users, watch what happens, and switch them off in seconds if anything looks wrong.
          </p>
          <div className="mt-8">
            <StatusChip status={status} lastUpdate={lastUpdate} />
          </div>
        </section>

        <section className="grid gap-4 sm:grid-cols-3">
          {[
            ['Feature flags', 'Gate any code path behind a switch you control from the console.'],
            ['Kill switch', 'Disable a misbehaving feature everywhere with one confirmed click.'],
            ['Live updates', 'Changes reach running apps in about a second, with no redeploy.'],
          ].map(([title, text]) => (
            <div key={title} className="rounded-2xl border border-white/10 bg-surface/70 p-5 backdrop-blur-xl">
              <h2 className="font-medium">{title}</h2>
              <p className="mt-2 text-sm text-zinc-400">{text}</p>
            </div>
          ))}
        </section>

        <section className="rounded-2xl border border-white/10 bg-surface/70 p-6 backdrop-blur-xl">
          <h2 className="text-lg font-semibold">How this page is wired</h2>
          <p className="mt-2 text-sm text-zinc-400">
            The banner above is gated by the flag <span className="font-mono text-zinc-200">{FLAG_KEY}</span>. Toggle or
            kill it in the console and it appears or disappears here without a refresh. The page evaluates the flag on
            load and again whenever Helios pushes a change, and hides the banner if Helios can't be reached.
          </p>
          <pre className="mt-4 overflow-x-auto rounded-lg border border-white/10 bg-black/50 p-4 font-mono text-[13px] leading-relaxed text-zinc-200">
            {SNIPPET}
          </pre>
        </section>
      </main>
    </div>
  )
}

import type { ReactNode } from 'react'

export function ConsoleCard({ prod, children }: { prod: boolean; children: ReactNode }) {
  return (
    <section
      className={`mt-4 animate-fade-up rounded-2xl border bg-surface/70 p-5 backdrop-blur-xl ${
        prod
          ? 'border-red-500/30 shadow-[0_0_80px_-30px_rgba(239,68,68,0.5)]'
          : 'border-white/10 shadow-[0_0_80px_-30px_rgba(124,92,255,0.45)]'
      }`}
      style={{ animationDelay: '0.1s' }}
    >
      {children}
    </section>
  )
}

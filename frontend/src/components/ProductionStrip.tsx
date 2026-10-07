export function ProductionStrip({ show }: { show: boolean }) {
  if (!show) return null
  return (
    <div className="mb-6 rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-2 text-center text-xs font-semibold uppercase tracking-widest text-red-300">
      Production: changes affect live users
    </div>
  )
}

export function Logo({ className = '' }: { className?: string }) {
  return (
    <span className={`inline-flex items-center gap-2 font-semibold tracking-tight ${className}`}>
      <span className="relative flex h-6 w-6 items-center justify-center rounded-md bg-gradient-to-br from-accent to-indigo-400 shadow-[0_0_18px_rgba(124,92,255,0.6)]">
        <span className="h-2 w-2 rounded-full bg-white/90" />
      </span>
      <span>
        Helios <span className="font-normal text-zinc-400">console</span>
      </span>
    </span>
  )
}

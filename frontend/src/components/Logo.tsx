import { useId } from 'react'

interface MarkProps {
  /** Width and height of the square mark, in px. */
  size?: number
  /** `color` is the violet gradient; `mono` is all white, for use on coloured backgrounds. */
  variant?: 'color' | 'mono'
  /** Accessible name when the mark stands alone. Leave out when the wordmark sits beside it. */
  label?: string
}

/**
 * The Helios mark: a toggle track with the sun at its "on" end. Same geometry as
 * public/logo-mark.svg and public/logo-mark-mono.svg. Inline, so it costs no request.
 */
export function LogoMark({ size = 24, variant = 'color', label }: MarkProps) {
  const uid = useId() // ids must be unique when several marks are on one page
  const gradient = `${uid}-g`
  const mask = `${uid}-m`
  const a11y = label ? { role: 'img' as const, 'aria-label': label } : { 'aria-hidden': true as const }

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      className="shrink-0"
      style={variant === 'color' ? { filter: 'drop-shadow(0 0 6px rgba(124, 92, 255, 0.55))' } : undefined}
      {...a11y}
    >
      {variant === 'color' ? (
        <>
          <defs>
            <linearGradient id={gradient} x1="1" y1="0" x2="63" y2="0" gradientUnits="userSpaceOnUse">
              <stop offset="0" stopColor="#7c5cff" />
              <stop offset="1" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <rect x="1" y="16" width="62" height="32" rx="16" fill={`url(#${gradient})`} />
          <circle cx="45.5" cy="32" r="16" fill="#fff" stroke={`url(#${gradient})`} strokeWidth="3.5" />
        </>
      ) : (
        <>
          <defs>
            <mask id={mask}>
              <rect width="64" height="64" fill="#000" />
              <rect x="1" y="16" width="62" height="32" rx="16" fill="#fff" />
              <circle cx="45.5" cy="32" r="19.5" fill="#000" />
            </mask>
          </defs>
          <rect width="64" height="64" fill="#fff" mask={`url(#${mask})`} />
          <circle cx="45.5" cy="32" r="16" fill="#fff" />
        </>
      )}
    </svg>
  )
}

interface LogoProps extends Pick<MarkProps, 'size' | 'variant'> {
  /** Show the "Helios" wordmark beside the mark. */
  wordmark?: boolean
  /** The muted word after the wordmark. Pass null to leave it out. */
  suffix?: string | null
  className?: string
}

/** Mark + "Helios" wordmark. The defaults match how the brand has always been shown: "Helios console". */
export function Logo({ size = 24, variant = 'color', wordmark = true, suffix = 'console', className = '' }: LogoProps) {
  return (
    <span className={`inline-flex items-center gap-2 font-semibold tracking-tight ${className}`}>
      <LogoMark size={size} variant={variant} label={wordmark ? undefined : 'Helios'} />
      {wordmark && (
        <span>
          Helios{suffix ? <> <span className="font-normal text-zinc-400">{suffix}</span></> : null}
        </span>
      )}
    </span>
  )
}

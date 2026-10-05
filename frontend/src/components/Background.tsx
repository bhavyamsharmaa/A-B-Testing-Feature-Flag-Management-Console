// Fixed, non-interactive animated backdrop: three slow drifting glow orbs and
// a faint moving grid, faded out toward the edges. Pure CSS, GPU transforms.
export function Background() {
  return (
    <div aria-hidden className="pointer-events-none fixed inset-0 -z-10 overflow-hidden">
      <div className="absolute -left-[10vw] -top-[15vh] h-[55vmax] w-[55vmax] animate-orb-a rounded-full bg-accent/25 blur-[120px]" />
      <div className="absolute -right-[15vw] top-[10vh] h-[48vmax] w-[48vmax] animate-orb-b rounded-full bg-indigo-500/20 blur-[130px]" />
      <div className="absolute -bottom-[25vh] left-[20vw] h-[42vmax] w-[42vmax] animate-orb-c rounded-full bg-fuchsia-500/10 blur-[140px]" />
      <div
        className="absolute inset-0 animate-grid-drift opacity-[0.35]"
        style={{
          backgroundImage:
            'linear-gradient(to right, rgba(255,255,255,0.05) 1px, transparent 1px), linear-gradient(to bottom, rgba(255,255,255,0.05) 1px, transparent 1px)',
          backgroundSize: '56px 56px',
          maskImage: 'radial-gradient(ellipse 70% 60% at 50% 40%, black 30%, transparent 100%)',
          WebkitMaskImage: 'radial-gradient(ellipse 70% 60% at 50% 40%, black 30%, transparent 100%)',
        }}
      />
    </div>
  )
}

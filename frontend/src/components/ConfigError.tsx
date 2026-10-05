export function ConfigError({ missing }: { missing: string[] }) {
  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-lg rounded-xl border border-red-500/40 bg-surface p-6">
        <h1 className="text-lg font-semibold text-red-400">Console is not configured</h1>
        <p className="mt-2 text-sm text-zinc-400">
          The following environment {missing.length === 1 ? 'variable is' : 'variables are'} missing or empty:
        </p>
        <ul className="mt-3 space-y-1 font-mono text-sm text-zinc-100">
          {missing.map((name) => (
            <li key={name}>{name}</li>
          ))}
        </ul>
        <p className="mt-4 text-sm text-zinc-400">
          Set {missing.length === 1 ? 'it' : 'them'} in <span className="font-mono">frontend/.env.local</span> for
          local dev, or in the Vercel project settings, then rebuild. See{' '}
          <span className="font-mono">.env.example</span>.
        </p>
      </div>
    </main>
  )
}

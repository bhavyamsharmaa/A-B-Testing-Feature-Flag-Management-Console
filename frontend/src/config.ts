// All build-time configuration. Vite inlines VITE_* variables into the bundle,
// so only public values belong here (never a secret or service_role key).

const REQUIRED = [
  'VITE_API_BASE_URL',
  'VITE_SUPABASE_URL',
  'VITE_SUPABASE_PUBLISHABLE_KEY',
] as const

const env = import.meta.env

export const missingEnv: string[] = REQUIRED.filter((name) => !env[name]?.trim())

export const config = {
  apiBaseUrl: (env.VITE_API_BASE_URL ?? '').trim().replace(/\/+$/, ''),
  supabaseUrl: (env.VITE_SUPABASE_URL ?? '').trim(),
  supabasePublishableKey: (env.VITE_SUPABASE_PUBLISHABLE_KEY ?? '').trim(),
}

// Public demo page only (/demo). Deliberately not in REQUIRED: the console must
// keep working without it. Like every VITE_* value it ships in the bundle.
export const heliosSdkKey = (env.VITE_HELIOS_SDK_KEY ?? '').trim()

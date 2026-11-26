import { defineConfig } from '@playwright/test'

// End-to-end tests drive the real console against a LOCAL stack: the Go
// backend on :8080, the devauth stand-in on :54321 and a throwaway Postgres.
// See docs/LOCAL_MULTITENANCY.md for how to start it. They never touch Supabase.
export default defineConfig({
  testDir: './e2e',
  timeout: 120_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:5173',
    viewport: { width: 1280, height: 900 },
    trace: 'retain-on-failure',
  },
  outputDir: './test-results',
})

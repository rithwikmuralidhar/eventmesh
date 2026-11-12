import { defineConfig, devices } from '@playwright/test'

// The stack (API serving the built frontend, worker, receiver) is started by
// scripts/test-e2e.ps1 with short retry delays; this config only points at it.
export default defineConfig({
  testDir: './e2e',
  timeout: 90_000,
  expect: { timeout: 20_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.EM_E2E_URL ?? 'http://127.0.0.1:8090',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})

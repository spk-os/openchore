import { defineConfig } from '@playwright/test';

// TX-PWA-E2E: the production-build Service Worker pipeline.
//
// Unlike playwright.config.ts (Vite dev server, no SW — registerServiceWorker
// skips non-PROD builds), this config serves the real `vite build` output
// through `vite preview`, where web/public/openchore-sw.js is served and
// registered, and drives it with real Chromium. The API runs on its own port
// against a throwaway /tmp DB; the preview server proxies /api, /uploads and
// /tts to it (web/vite.config.ts preview.proxy).
//
// Ports are overridable so parallel checkouts can run side by side:
// E2E_PREVIEW_PORT=4185 E2E_PWA_API_PORT=8195 npx playwright test
// --config=playwright.pwa.config.ts
const PREVIEW_PORT = process.env.E2E_PREVIEW_PORT || '4175';
const API_PORT = process.env.E2E_PWA_API_PORT || '8187';
// Optional: use an already-installed Chromium instead of Playwright's build.
const CHROMIUM_PATH = process.env.E2E_CHROMIUM_PATH;

// A throwaway DB per run: /tmp/openchore-pwa-<pid>.db. Never the checkout's
// openchore.db — this suite must not wipe or share any real database, and
// nothing in this config may rm anything.
const DB_PATH = `/tmp/openchore-pwa-${process.pid}.db`;

export default defineConfig({
  testDir: './tests',
  // Only the PWA spec(s): the rest of the suite runs against the dev server
  // (playwright.config.ts) and is unaffected.
  testMatch: /pwa-sw\.spec\.ts/,
  timeout: 180_000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['json', { outputFile: 'pwa-report.json' }]],
  use: {
    baseURL: `http://localhost:${PREVIEW_PORT}`,
    headless: true,
    screenshot: 'only-on-failure',
    // 失败时保留 trace，供独立审查定位真实浏览器里的时序/缓存现场。
    trace: 'retain-on-failure',
    ...(CHROMIUM_PATH ? { launchOptions: { executablePath: CHROMIUM_PATH } } : {}),
  },
  projects: [
    {
      name: 'pwa',
      use: { browserName: 'chromium' },
    },
  ],
  webServer: [
    {
      // Fresh SQLite DB under /tmp (DB_PATH), seeded from the example config.
      // No `rm` anywhere: the pid-suffixed path starts nonexistent.
      command: `cd .. && go run cmd/server/main.go`,
      port: Number(API_PORT),
      env: {
        PORT: API_PORT,
        DB_PATH,
        CONFIG_PATH: 'config/config.example.yaml',
      },
      reuseExistingServer: false,
      timeout: 60_000,
    },
    {
      // Real production bundle: build first, then serve it with vite preview.
      // --strictPort keeps an accidental 4173 occupier from silently winning.
      command: `cd ../web && npm run build && npx vite preview --port ${PREVIEW_PORT} --strictPort`,
      port: Number(PREVIEW_PORT),
      env: { VITE_PREVIEW_API_TARGET: `http://localhost:${API_PORT}` },
      reuseExistingServer: false,
      timeout: 180_000,
    },
  ],
});

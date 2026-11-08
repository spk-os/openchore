import { defineConfig } from '@playwright/test';

// Ports are overridable so several checkouts (e.g. git worktrees) can run the
// suite side by side: E2E_API_PORT=8181 E2E_WEB_PORT=5281 npx playwright test
const API_PORT = process.env.E2E_API_PORT || '8080';
const WEB_PORT = process.env.E2E_WEB_PORT || '5173';
// Optional: use an already-installed Chromium instead of Playwright's pinned build.
const CHROMIUM_PATH = process.env.E2E_CHROMIUM_PATH;
// The app defaults to Chinese on a clean browser (i18n FALLBACK_CODE='zh',
// detection order is localStorage-only). The e2e specs assert English UI
// labels ('Select profile for Alex', 'Manage', …), so every context gets
// localStorage `openchore_lang=en` injected via a cookie-less storageState.
// E2E_LANG=zh is only for manually debugging the Chinese UI — with it set,
// the English-label specs (including screenshots.spec) fail by design.
// Production screenshots for the wiki still run under the default en.
const E2E_LANG = process.env.E2E_LANG || 'en';
const storageState = {
  cookies: [],
  origins: [
    {
      origin: `http://localhost:${WEB_PORT}`,
      localStorage: [{ name: 'openchore_lang', value: E2E_LANG }],
    },
  ],
};

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  retries: 0,
  use: {
    baseURL: `http://localhost:${WEB_PORT}`,
    headless: true,
    screenshot: 'only-on-failure',
    trace: 'on-first-retry',
    storageState,
    ...(CHROMIUM_PATH ? { launchOptions: { executablePath: CHROMIUM_PATH } } : {}),
  },
  projects: [
    {
      name: 'main',
      use: { browserName: 'chromium' },
      // The PWA spec runs only under playwright.pwa.config.ts (production
      // build + vite preview + SW); it must not run against the dev server.
      testIgnore: /admin-pin-change|sign-in-options|pwa-sw/,
    },
    {
      // Specs that change household-wide sign-in state, run after the rest.
      name: 'pin-change',
      use: { browserName: 'chromium' },
      testMatch: /admin-pin-change|sign-in-options/,
      dependencies: ['main'],
    },
  ],
  webServer: [
    {
      command: 'cd .. && rm -f openchore.db openchore.db-shm openchore.db-wal && cp -n config/config.example.yaml config/config.yaml 2>/dev/null; go run cmd/server/main.go',
      port: Number(API_PORT),
      // The points-decay worker ticks every 15 minutes by default, which no
      // test can wait for. Only kids with decay explicitly enabled are
      // touched, so a fast tick is inert for every other spec.
      env: { POINTS_DECAY_INTERVAL: '2s', PORT: API_PORT },
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
    },
    {
      command: `cd ../web && npm run dev -- --port ${WEB_PORT} --strictPort`,
      port: Number(WEB_PORT),
      env: { VITE_API_TARGET: `http://localhost:${API_PORT}` },
      reuseExistingServer: !process.env.CI,
      timeout: 15_000,
    },
  ],
});

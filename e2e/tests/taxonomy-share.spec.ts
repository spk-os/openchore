// 042 taxonomy — public share link: create → read with no session (API + UI)
// → admin revoke → the link is gone (API 404 + UI missing state).
//
// Final contract (coordinator): POST .../share answers
// {share_id, url:"/share/taxonomy/{token}", token, expires_at}; DELETE
// /api/admin/taxonomy/shares/{share_id}. The public read is token-addressed
// and needs no cookie, which the anonymous browser context proves.
import { test, expect } from '@playwright/test';
import {
  createShare,
  createWeakFinishedDiagnostic,
  findUser,
  revokeShare,
} from './helpers/taxonomy';

const KID = 'Emma';

test.describe('042 taxonomy — public share', () => {
  test('a share opens without a session and is unavailable after revoke', async ({ page, browser }, testInfo) => {
    const kid = await findUser(page, KID);
    const weak = await createWeakFinishedDiagnostic(page, kid.id);
    const share = await createShare(page, weak.session.id);

    expect(share.share_id).toBeGreaterThan(0);
    const token = share.token || share.url.split('/').filter(Boolean).pop() || '';
    expect(token, 'share token').toBeTruthy();
    expect(share.url).toBe(`/share/taxonomy/${token}`);

    // No cookies: a fresh context has no openchore session at all.
    const baseURL =
      (testInfo.project.use.baseURL as string | undefined) ??
      `http://localhost:${process.env.E2E_WEB_PORT || '5173'}`;
    const anon = await browser.newContext();
    const anonPage = await anon.newPage();

    const publicRead = await anon.request.get(`${baseURL}/api/taxonomy/shared/${encodeURIComponent(token)}`);
    expect(publicRead.status()).toBe(200);
    const body = (await publicRead.json()) as Record<string, unknown>;
    // The public API may answer flat or wrapped; either way it must carry the
    // report payload and never the raw session state.
    const snapshot = (body.snapshot ?? body) as Record<string, unknown>;
    expect(snapshot.subject ?? snapshot.session_id).toBeTruthy();

    await anonPage.goto(`${baseURL}/share/taxonomy/${encodeURIComponent(token)}`);
    await expect(anonPage.locator('[data-test="tx-shared"]')).toBeVisible({ timeout: 10_000 });
    await expect(anonPage.locator('[data-test="tx-share-missing"]')).toHaveCount(0);

    // Admin revoke (id comes from the create response, not a guessed id).
    await revokeShare(page, share.share_id);

    const revokedRead = await anon.request.get(
      `${baseURL}/api/taxonomy/shared/${encodeURIComponent(token)}`,
    );
    expect([404, 410]).toContain(revokedRead.status());

    await anonPage.reload();
    await expect(anonPage.locator('[data-test="tx-share-missing"]')).toBeVisible({ timeout: 10_000 });
    await expect(anonPage.locator('[data-test="tx-shared"]')).toHaveCount(0);

    await anon.close();
  });
});

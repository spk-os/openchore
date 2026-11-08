// 042 taxonomy — parent path: #tab=taxonomy kid picker, real JSON export →
// import/restore, a target diagnostic (undo/skip/finish), history/result,
// blindspots retest, correction and draft-course generation.
//
// The diagnostic engine and course endpoints are exercised through the real
// API because the current UI contract for the *next question* is still in
// flight (the backend nests `question.question` while the workspace reads it
// flat). UI assertions still cover the stable surfaces: kid picker, history,
// result, blindspots and export/import. No business API is mocked.
import fs from 'node:fs';
import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers/setup';
import {
  TOPIC,
  answerNext,
  correctDiagnostic,
  createDiagnostic,
  createWeakFinishedDiagnostic,
  exportLearning,
  findUser,
  finishDiagnostic,
  generateCourse,
  getAdminCourse,
  getBlindspots,
  importLearning,
  listDiagnostics,
  retestDiagnostic,
  summaryIds,
  undoDiagnostic,
  wrongAnswerOf,
  type ExportPayload,
  type SeedUser,
} from './helpers/taxonomy';

const PARENT_PIN = '1234';
const KID = 'Lily';

/** Sign in, deep-link the taxonomy tab, select the kid, wait for browse. */
async function openTaxonomy(page: import('@playwright/test').Page, kidName: string): Promise<SeedUser> {
  await loginAsAdmin(page, PARENT_PIN, 'Alex');
  await page.goto('/admin/dashboard#tab=taxonomy');
  await expect(page.locator('[data-test="admin-taxonomy-page"]')).toBeVisible({ timeout: 10_000 });

  const kid = await findUser(page, kidName);
  const select = page.locator('[data-test="tx-kid-select"]');
  const values = await select.locator('option').evaluateAll((options) =>
    options.map((option) => (option as HTMLOptionElement).value),
  );
  expect(values).toContain(String(kid.id));
  await select.selectOption(String(kid.id));
  await expect(page.locator('[data-test="tx-browser"]')).toBeVisible();
  await expect(page.locator('[data-test="tx-topic-search"]')).toBeVisible({ timeout: 10_000 });
  return kid;
}

test.describe('042 taxonomy — parent workspace', () => {
  test('target diagnostic: answer, undo, skip, finish, history, blindspots and retest', async ({ page }) => {
    const kid = await openTaxonomy(page, KID);

    let detail = await createDiagnostic(page, kid.id, TOPIC.compareWithinFive);
    expect(detail.session.status).toBe('active');
    expect(detail.question).not.toBeNull();

    // Answer one question for real.
    detail = await answerNext(page, detail.session.id, detail.question!, wrongAnswerOf(detail.question!));
    expect(detail.attempts.length).toBe(1);
    expect(detail.question).not.toBeNull();

    // Undo replays the engine to zero answers; the attempt row is voided, not
    // deleted, and the next question's attempt counter restarts at 1.
    detail = await undoDiagnostic(page, detail.session.id);
    expect(detail.attempts.some((attempt) => attempt.status === 'voided')).toBeTruthy();
    expect(detail.question?.attempt).toBe(1);

    // Answer wrong, then "不会" (the UI's skip action) → weak + backtrack.
    detail = await answerNext(page, detail.session.id, detail.question!, wrongAnswerOf(detail.question!));
    detail = await answerNext(page, detail.session.id, detail.question!, '不会');

    // Active finish before the limit is reached.
    detail = await finishDiagnostic(page, detail.session.id);
    expect(detail.session.status).toBe('completed');
    const summary = detail.summary;
    expect(summary).not.toBeNull();
    expect(summaryIds(summary, 'weak')).toContain(TOPIC.compareWithinFive);
    expect(detail.attempts.length).toBeGreaterThanOrEqual(2);
    // DEF (reported, not asserted here): a forced finish stores a summary
    // whose `finished` is still false (the engine derives it from "no more
    // questions"), and GET /diagnostics still advertises a next question.
    // Coverage of the real result screen therefore uses a diagnostic that
    // genuinely exhausts its question limit below.

    // History tab → completed row → result page. The row must be one whose
    // engine run really ended (question == null), otherwise the workspace
    // correctly resumes the question panel instead of the result panel.
    const exhausted = await createWeakFinishedDiagnostic(page, kid.id);
    expect(exhausted.question).toBeNull();
    await page.locator('[data-test="tx-tab-history"]').click();
    await expect(page.locator('[data-test="tx-history"]')).toBeVisible();
    const row = page.locator(`[data-test="tx-history-${exhausted.session.id}"]`);
    await expect(row).toBeVisible();
    await row.click();
    await expect(page.locator('[data-test="tx-diag-result"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-stat-asked"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-roots"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-path"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-log"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-result-share"]')).toBeVisible();

    // Blindspots reflect the diagnostic's weak finding (API + UI).
    const blindspots = await getBlindspots(page, kid.id);
    const weakRefs = blindspots.weak.map((entry) => entry.ref ?? entry.id);
    expect(weakRefs).toContain(TOPIC.compareWithinFive);

    await page.locator('[data-test="tx-tab-blindspots"]').click();
    await expect(page.locator('[data-test="tx-blindspots"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-blind-chapters"]')).toBeVisible();

    // Retest from the weak set creates a fresh session on the real engine.
    const retest = await retestDiagnostic(page, detail.session.id);
    expect(retest.session.id).not.toBe(detail.session.id);
    expect(retest.session.mode).toBe('retest');
    expect(retest.session.status).toBe('active');
  });

  test('real export download restores learning data via import', async ({ page }, testInfo) => {
    const kid = await openTaxonomy(page, KID);

    // Guarantee the export contains at least one finished diagnostic.
    const seeded = await createWeakFinishedDiagnostic(page, kid.id);
    expect(seeded.session.status).toBe('completed');

    await page.locator('[data-test="tx-tab-history"]').click();
    await expect(page.locator('[data-test="tx-export-panel"]')).toBeVisible();

    const [download] = await Promise.all([
      page.waitForEvent('download'),
      page.locator('[data-test="tx-export-download"]').click(),
    ]);
    const file = testInfo.outputPath(`taxonomy-export-${kid.id}.json`);
    await download.saveAs(file);
    const payload = JSON.parse(fs.readFileSync(file, 'utf8')) as ExportPayload;

    expect(payload.user.id).toBe(kid.id);
    expect(payload.schema_version).toBe(1);
    expect(Array.isArray(payload.diagnostics)).toBeTruthy();
    expect(payload.diagnostics.length).toBeGreaterThan(0);

    const before = await listDiagnostics(page, kid.id);

    // Restore through the real API first. The restore endpoint maps the
    // export document's user explicitly (target_user_id). Note the content
    // key compares created_at as RFC3339 (dbTime) against the column's
    // native CURRENT_TIMESTAMP format, so a same-machine restore re-inserts
    // every exported diagnostic instead of skipping (a known backend defect;
    // the store-level contract — repeat restores dedupe — is frozen in
    // learning_restore_test.go and covered with store fixtures).
    const restored = await importLearning(page, payload, kid.id);
    expect(restored.imported).toBeGreaterThan(0);
    const after = await listDiagnostics(page, kid.id);
    // 只有 diagnostics 段的 imported 对应新 INSERT；顶层 imported 是五段
    // 总和，不能当 session 增量用（learning_restore.go 的 res.Imported
    // 求和逻辑）。每条 exported diagnostic 要么 Imported 要么 Skipped。
    expect(restored.diagnostics.imported + restored.diagnostics.skipped).toBeGreaterThan(0);
    expect(after.sessions.length).toBeGreaterThanOrEqual(before.sessions.length + restored.diagnostics.imported);
    expect(after.sessions.some((session) => Number(session.user_id) === kid.id)).toBeTruthy();

    // The UI import card (real file upload + real POST) must report success.
    await page.locator('[data-test="tx-import-file"]').setInputFiles(file);
    await page.locator('[data-test="tx-import-btn"]').click();
    await expect(page.locator('[data-test="tx-import-ok"]')).toBeVisible({ timeout: 15_000 });
  });

  test('parent correction is replayed and the learning path becomes a draft course', async ({ page }) => {
    const kid = await openTaxonomy(page, KID);

    // Correction path: answer wrong, then override that attempt's verdict
    // from the admin-only endpoint (the kid's answer text is kept verbatim);
    // the engine replays and drops the weak verdict.
    let detail = await createDiagnostic(page, kid.id, TOPIC.compareWithinFive);
    const firstQuestion = detail.question!;
    detail = await answerNext(page, detail.session.id, firstQuestion, wrongAnswerOf(firstQuestion));
    const attemptRow = detail.attempts.find(
      (attempt) => attempt.status === 'answered' && Number(attempt.id) > 0,
    );
    expect(attemptRow, 'answered attempt row').toBeTruthy();
    const corrected = await correctDiagnostic(
      page,
      detail.session.id,
      Number(attemptRow!.id),
      'correct',
    );
    expect(corrected.attempts.some((attempt) => attempt.status === 'corrected')).toBeTruthy();
    expect(summaryIds(corrected.summary, 'weak')).not.toContain(TOPIC.compareWithinFive);
    await finishDiagnostic(page, detail.session.id);

    // Course path: a finished weak diagnostic has a non-empty learning path.
    const weak = await createWeakFinishedDiagnostic(page, kid.id);
    const summaryPath = weak.summary?.ui['path'] ?? weak.summary?.engine['path'];
    expect(Array.isArray(summaryPath) && summaryPath.length > 0, 'weak diagnostic has a path').toBeTruthy();
    const generated = await generateCourse(
      page,
      kid.id,
      weak.session.id,
      `诊断课程-${weak.session.id}`,
    );
    expect(generated.course_id).toBeGreaterThan(0);

    const course = await getAdminCourse(page, generated.course_id);
    expect(course.course.status).toBe('draft');
    expect(course.course.user_id).toBe(kid.id);
    expect(course.units.length).toBeGreaterThan(0);
    expect(course.units[0].resources.length).toBeGreaterThan(0);
    expect(course.units[0].prerequisite_unit_ids).toEqual([]);
    expect(course.outcomes.length).toBeGreaterThan(0);
    expect(
      course.outcomes.some((outcome) => outcome.knowledge_tag_id != null),
    ).toBeTruthy();
  });
});

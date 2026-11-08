// 042 taxonomy — kid path: Today → /learn, all ten subjects, detail view and
// a full practice run (submit / reference answer self-check / skip).
//
// Selectors come from the components' data-test attributes; no copy text is
// used to find elements. The practice answers come from the static
// CORRECT_ANSWERS/WRONG_ANSWERS tables in helpers/taxonomy (the API strips
// `answer` from topic questions for everyone, not just kids), then are
// submitted through the real UI so the server judges and records them.
import { test, expect } from '@playwright/test';
import { selectUser } from './helpers/setup';
import {
  TOPIC,
  correctAnswerOf,
  exportLearning,
  findUser,
  getTopicDetail,
} from './helpers/taxonomy';

const KID = 'Emma';
const SUBJECTS = [
  'math',
  'chinese',
  'english',
  'physics',
  'chemistry',
  'science',
  'morality',
  'history',
  'geography',
  'biology',
] as const;

test.describe('042 taxonomy — kid learning', () => {
  test('Today card opens /learn and every subject browses', async ({ page }) => {
    await selectUser(page, KID);
    await expect(page.locator('[data-test="kid-home-page"]')).toBeVisible({ timeout: 10_000 });

    await page.locator('[data-test="learn-entry"]').click();
    await expect(page).toHaveURL('/learn');
    await expect(page.locator('[data-test="kid-learn-page"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-workspace"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-browser"]')).toBeVisible();

    // All ten subject chips are present.
    for (const subject of SUBJECTS) {
      await expect(page.locator(`[data-test="tx-subject-${subject}"]`)).toBeVisible();
    }

    // Math loads on mount (the workspace default).
    await expect(page.locator('[data-test="tx-topic-search"]')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('[data-test="tx-chapter"]').first()).toBeVisible();

    // Switch through the other nine; each click must produce its own real
    // topics response before asserting the list rendered.
    for (const subject of SUBJECTS.slice(1)) {
      const response = page.waitForResponse((resp) => {
        const url = new URL(resp.url());
        return (
          resp.request().method() === 'GET' &&
          url.pathname.endsWith('/api/taxonomy/topics') &&
          url.searchParams.get('subject') === subject &&
          resp.ok()
        );
      });
      await page.locator(`[data-test="tx-subject-${subject}"]`).click();
      await response;
      await expect(page.locator('[data-test="tx-topic-search"]')).toBeVisible();
      await expect(page.locator('[data-test="tx-chapter"]').first()).toBeVisible();
    }
  });

  test('detail view and practice cover submit, reference answer and skip', async ({ page }) => {
    await selectUser(page, KID);
    await page.goto('/learn');
    await expect(page.locator('[data-test="tx-browser"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-topic-search"]')).toBeVisible({ timeout: 10_000 });

    // Pin the deterministic grade-1 topic via the real search endpoint.
    await page.locator('[data-test="tx-topic-search"]').fill('1到5各数的认识');
    const topicButton = page.locator(`[data-test="tx-topic-${TOPIC.countToFive}"]`);
    await expect(topicButton).toBeVisible({ timeout: 10_000 });
    await topicButton.click();

    await expect(page.locator('[data-test="tx-topic-detail"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-detail-name"]')).toHaveText('1到5各数的认识');
    await expect(page.locator('[data-test="tx-prereq-graph"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-detail-questions"]')).toBeVisible();
    // cnm_p002 depends on cnm_p001: the local prerequisite graph/relations.
    await expect(page.locator('[data-test="tx-dependent-cnm_p002"]')).toBeVisible();

    const adminView = await getTopicDetail(page, TOPIC.countToFive);
    const questions = adminView.questions ?? [];
    expect(questions.length).toBeGreaterThanOrEqual(4);

    await page.locator('[data-test="tx-detail-practice"]').click();
    await expect(page.locator('[data-test="tx-practice"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-answer-input"]')).toBeVisible();

    // Q1: numeric seed question judged correct through the real judge.
    // (The seed pack's cnm_p001 questions are all numeric — the old
    // self-question tx-peek flow no longer matches the seeded data; the
    // self-question path stays covered by PracticeRunner unit tests.)
    const q1 = questions[0];
    expect(q1.id).toBeTruthy();
    await page.locator('[data-test="tx-answer-input"]').fill(correctAnswerOf(q1));
    const firstJudged = page.waitForResponse(
      (resp) => resp.request().method() === 'POST' && resp.url().includes('/api/taxonomy/practice') && resp.ok(),
    );
    await page.locator('[data-test="tx-submit"]').click();
    await firstJudged;
    await expect(page.locator('[data-test="tx-verdict"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-verdict"]')).toHaveText('回答正确');
    await page.locator('[data-test="tx-next"]').click();

    // Q2: "不会" path shows the reference answer, then advances.
    await page.locator('[data-test="tx-skip"]').click();
    await expect(page.locator('[data-test="tx-skip-next"]')).toBeVisible();
    await page.locator('[data-test="tx-skip-next"]').click();

    // Q3: deterministic numeric answer through the real judge.
    const q3 = questions[2];
    expect(q3.id).toBeTruthy();
    await expect(page.locator('[data-test="tx-answer-input"]')).toBeVisible();
    await page.locator('[data-test="tx-answer-input"]').fill(correctAnswerOf(q3));
    const judged = page.waitForResponse(
      (resp) => resp.request().method() === 'POST' && resp.url().includes('/api/taxonomy/practice') && resp.ok(),
    );
    await page.locator('[data-test="tx-submit"]').click();
    await judged;
    await expect(page.locator('[data-test="tx-verdict"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-next"]')).toBeVisible();
    await page.locator('[data-test="tx-next"]').click();

    // Q4: skip to the end.
    await expect(page.locator('[data-test="tx-skip"]')).toBeVisible();
    await page.locator('[data-test="tx-skip"]').click();
    await expect(page.locator('[data-test="tx-skip-next"]')).toBeVisible();
    await page.locator('[data-test="tx-skip-next"]').click();

    // Done view: the runner keeps its container but no question widgets remain.
    await expect(page.locator('[data-test="tx-practice"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-answer-input"]')).toHaveCount(0);
    await expect(page.locator('[data-test="tx-choices"]')).toHaveCount(0);

    // The judged Q3 answer rolled into the learner profile (durable row).
    const kid = await findUser(page, KID);
    const exported = await exportLearning(page, kid.id);
    expect(exported.user.id).toBe(kid.id);
    expect(exported.mastery.some((row) => row.external_ref === TOPIC.countToFive)).toBeTruthy();
  });
});

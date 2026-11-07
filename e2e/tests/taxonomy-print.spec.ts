// 042 taxonomy — FR-12(c) 打印友好视图验收（诊断报告 / 盲区清单 / 学习路径）。
//
// 三个报告视图共享屏幕 DOM，打印态由 workspace.module.css 的 @media print
// 控制（隐藏导航/筛选/按钮等交互控件 + 分页/不吞字规则）。本 spec 用
// Playwright 的 print 媒体仿真对三个视图做打印态断言：
//   - 打印按钮存在（diagnose/blindspots 标签）；
//   - print media 下 .noPrint（打印按钮）与标签导航（.subjectChips 类的
//     data-test=tx-tab-*）不可见；
//   - 报告主体可见（结果卡 tx-diag-result / 盲区 tx-blindspots）；
//   - 打印态无横向溢出（scrollWidth 不超出 clientWidth + 容差）。
//
// 数据路径全部走真实 API（复用 helpers/taxonomy 的引擎驱动助手）：先在
// API 侧制造一次真跑完的 weak 诊断，家长端从历史打开结果（诊断报告 +
// 学习路径同页），盲区清单由同一份 weak findings 聚合而来。真实 Chromium
// 截图验收由最终统一验收链实跑，本文件不自带任何会删库的 webServer。
import { test, expect, type Page } from '@playwright/test';
import { loginAsAdmin } from './helpers/setup';
import {
  TOPIC,
  createWeakFinishedDiagnostic,
  findUser,
} from './helpers/taxonomy';

const KID = 'Lily';

/** 家长端打开 taxonomy 标签并选中孩子，返回孩子的 seed 记录。 */
async function openTaxonomy(page: Page, kidName: string) {
  await loginAsAdmin(page);
  await page.goto('/admin/dashboard#tab=taxonomy');
  await expect(page.locator('[data-test="admin-taxonomy-page"]')).toBeVisible({ timeout: 10_000 });

  const kid = await findUser(page, kidName);
  const select = page.locator('[data-test="tx-kid-select"]');
  await select.selectOption(String(kid.id));
  await expect(page.locator('[data-test="tx-browser"]')).toBeVisible();
  return kid;
}

/** 断言打印媒体下的通用形态：导航隐藏、无横向溢出，并落一张截图。 */
async function assertPrintShape(page: Page, shotName: string) {
  await page.emulateMedia({ media: 'print' });

  // 标签导航（subjectChips 类容器）在 print 下 display:none。
  const tabs = page.locator('[data-test^="tx-tab-"]');
  const count = await tabs.count();
  for (let i = 0; i < count; i += 1) {
    await expect(tabs.nth(i)).toBeHidden();
  }

  // 打印按钮自身（.noPrint）在 print 下不可见。
  await expect(page.locator('[data-test="tx-print"]')).toBeHidden();

  // 无横向溢出：滚动宽度不得超出视口（容忍 2px 取整误差）。
  const overflow = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    client: document.documentElement.clientWidth,
  }));
  expect(overflow.scroll).toBeLessThanOrEqual(overflow.client + 2);

  await page.screenshot({ path: `taxonomy-print/${shotName}.png`, fullPage: true });

  // 还原屏幕媒体，避免影响同 worker 内后续用例。
  await page.emulateMedia({ media: null });
}

test.describe('042 taxonomy — FR-12 print-friendly views', () => {
  test('diagnosis report and learning path print without chrome or overflow', async ({ page }) => {
    const kid = await openTaxonomy(page, KID);

    // 真跑完的 weak 诊断：结果页同时承载「诊断报告」与「推荐学习路径」。
    const finished = await createWeakFinishedDiagnostic(page, kid.id, TOPIC.compareWithinFive);
    expect(finished.session.status).toBe('completed');

    // 历史标签 → 该会话行 → 结果页（诊断报告 + 学习路径同页）。
    await page.locator('[data-test="tx-tab-history"]').click();
    await expect(page.locator('[data-test="tx-history"]')).toBeVisible();
    await page.locator(`[data-test="tx-history-${finished.session.id}"]`).click();

    await expect(page.locator('[data-test="tx-diag-result"]')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('[data-test="tx-roots"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-path"]')).toBeVisible();
    // 打印按钮在屏幕媒体下可见。
    await expect(page.locator('[data-test="tx-print"]')).toBeVisible();

    await assertPrintShape(page, '01-diagnosis-report');
    // 打印态报告主体仍可见（emulateMedia 复原前先断言内容在打印媒体下没被藏掉）。
    await page.emulateMedia({ media: 'print' });
    await expect(page.locator('[data-test="tx-diag-result"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-path"]')).toBeVisible();
    await page.emulateMedia({ media: null });
  });

  test('blindspot list prints without chrome or overflow', async ({ page }) => {
    const kid = await openTaxonomy(page, KID);

    // 盲区清单来自诊断 findings：先真跑一次 weak 诊断再进盲区标签。
    await createWeakFinishedDiagnostic(page, kid.id, TOPIC.compareWithinFive);
    await page.locator('[data-test="tx-tab-blindspots"]').click();
    await expect(page.locator('[data-test="tx-blindspots"]')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('[data-test="tx-print"]')).toBeVisible();

    await assertPrintShape(page, '02-blindspot-list');
    await page.emulateMedia({ media: 'print' });
    await expect(page.locator('[data-test="tx-blindspots"]')).toBeVisible();
    await page.emulateMedia({ media: null });
  });

  test('print button is scoped to the report tabs', async ({ page }) => {
    // 浏览与历史标签是交互工作流：不显示打印按钮（防止空态/加载态误显）。
    await openTaxonomy(page, KID);
    await expect(page.locator('[data-test="tx-browser"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-print"]')).toHaveCount(0);

    await page.locator('[data-test="tx-tab-history"]').click();
    await expect(page.locator('[data-test="tx-history"]')).toBeVisible();
    await expect(page.locator('[data-test="tx-print"]')).toHaveCount(0);
  });
});

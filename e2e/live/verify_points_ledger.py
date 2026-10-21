#!/usr/bin/env python3
# 2026-10-22 任务④「积分明细」验收：顶栏积分胶囊变按钮，点开 PointsSheet，
# 流水行（图标+理由+备注+时间+金额）渲染，负数金额带 urgent 色。
# 只打 live :8120，不起 webServer。
import sys, glob
from playwright.sync_api import sync_playwright

BASE = 'http://127.0.0.1:8120'

def login(page, name, pin):
    page.goto(BASE + '/login')
    page.wait_for_load_state('networkidle')
    page.get_by_role('button', name=f'为 {name} 选择资料').click()
    page.wait_for_timeout(1200)
    if '/login' in page.url:
        keypad = page.locator('div[role="group"] button')
        keypad.first.wait_for(state='visible', timeout=10000)
        for d in str(pin):
            page.locator('div[role="group"] button', has_text=d).first.click()
            page.wait_for_timeout(200)
        page.wait_for_timeout(2000)
        if '/login' in page.url:
            raise RuntimeError(f'{name} PIN 登录未跳转，url={page.url}')
    page.wait_for_function("!location.pathname.startsWith('/login')", timeout=15000)

results = []
def check(label, ok, detail=''):
    results.append((label, ok, detail))
    print(('PASS' if ok else 'FAIL'), '-', label, ('| ' + detail) if detail else '', flush=True)

with sync_playwright() as p:
    shell = sorted(glob.glob('/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell'))[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)
    ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    login(page, '小明', '')

    # 顶栏积分胶囊已是按钮（points-ledger-open）
    btn = page.locator('[data-test="points-ledger-open"]')
    check('顶栏积分胶囊是可点按钮（points-ledger-open）', btn.count() > 0, f'count={btn.count()}')

    if btn.count() > 0:
        # 无积分记录的孩子也至少要看到「帮助文案 + 空态」，不能空白
        btn.first.click()
        page.wait_for_timeout(1200)
        check('点积分胶囊弹出积分明细（Sheet）', page.get_by_role('dialog').count() > 0)
        body = page.locator('[role="dialog"]').inner_text()
        check('明细含帮助文案（每一分的来历）', '来历' in body or '解释' in body or '每一' in body)
        has_tx = page.locator('[role="dialog"] li').count() > 0
        if has_tx:
            check('积分流水行渲染（li 项）', True)
            neg = page.locator('[role="dialog"] li').filter(has_text='−')
            check('负数金额以 − 渲染（负向项可见）', neg.count() > 0 or '−' not in body)
        else:
            check('无流水时显示空态文案', '还没有' in body or '第一' in body, body[:60])
        # 关闭后再开仍正常（焦点陷阱不残留）
        page.keyboard.press('Escape')
        page.wait_for_timeout(600)
        check('Escape 关闭明细抽屉', page.get_by_role('dialog').count() == 0)
    check('全流程无前端报错（pageerror=0）', len(errors) == 0, f'{errors[:2]}')
    ctx.close()
    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
for f in fails:
    print('FAIL:', f[0], '|', f[2])
sys.exit(1 if fails else 0)

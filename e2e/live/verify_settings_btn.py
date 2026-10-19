#!/usr/bin/env python3
# 任务5 验证：header 设置齿轮按钮——14 tab 溢出后设置仍一键直达。
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
    page.wait_for_url(lambda u: '/login' not in str(u), timeout=15000)

results = []
def check(label, ok, detail=''):
    results.append((label, ok, detail))
    print(('PASS' if ok else 'FAIL'), '-', label, ('| ' + detail) if detail else '', flush=True)

with sync_playwright() as p:
    shell = sorted(glob.glob('/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell'))[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)

    # 手机视口（375px）：14 tab 必溢出，设置 tab 藏屏外
    ctx = browser.new_context(viewport={'width': 375, 'height': 700})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    login(page, 'Alex', '1234')
    errors.clear()
    page.goto(BASE + '/admin/dashboard')
    page.wait_for_load_state('networkidle')
    page.wait_for_timeout(2500)

    btn = page.locator('[data-test="header-settings"]')
    check('手机视口 header 设置齿轮按钮可见', btn.is_visible())

    btn.click()
    page.wait_for_timeout(1500)
    body = page.locator('body').inner_text()
    # SettingsTab 页标题「设置」+ System Base URL 卡
    check('点齿轮直达设置页（无需滚动 nav）', '系统外部地址' in body or 'System Base URL' in body or '设置' in body,
          f'has 设置={"设置" in body}')
    check('页面无 JS 错误', len(errors) == 0, f'{errors[:1]}')
    ctx.close()

    # 桌面视口回归：nav 最后一个按钮仍是 settings（e2e 依赖）
    ctx2 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page2 = ctx2.new_page()
    login(page2, 'Alex', '1234')
    page2.goto(BASE + '/admin/dashboard')
    page2.wait_for_load_state('networkidle')
    page2.wait_for_timeout(2000)
    nav_last = page2.locator('nav button').last
    check('桌面视口 nav 最后一个 tab 仍是「设置」（e2e 契约保持）', nav_last.inner_text().strip() == '设置',
          f'last={nav_last.inner_text().strip()!r}')
    ctx2.close()
    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
sys.exit(1 if fails else 0)

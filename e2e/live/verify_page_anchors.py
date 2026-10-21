#!/usr/bin/env python3
# live 校验：OD spk-openchore 契约 §6.2 —— admin 14 tab 每页根节点
# data-test="admin-<tab>-page" 与孩子端 4 页 -page 锚点全部就位。
# 只打 live :8120；桌面视口走侧边栏逐项点入（1024px 桌面形态）。
import sys, glob
from playwright.sync_api import sync_playwright

BASE = 'http://127.0.0.1:8120'

ADMIN_TABS = [  # (tab id, 期望根 data-test)
    ('kids-status', 'admin-home-page'),
    ('approvals',   'admin-approvals-page'),
    ('media',       'admin-media-page'),
    ('chores',      'admin-chores-page'),
    ('templates',   'admin-templates-page'),
    ('courses',     'admin-courses-page'),
    ('profile',     'admin-profile-page'),
    ('errorbook',   'admin-errorbook-page'),
    ('measures',    'admin-measures-page'),
    ('rewards',     'admin-rewards-page'),
    ('points',      'admin-points-page'),
    ('activity',    'admin-activity-page'),
    ('users',       'admin-users-page'),
    ('settings',    'admin-settings-page'),
]

results = []
def check(label, ok, detail=''):
    results.append((label, ok, detail))
    print(('PASS' if ok else 'FAIL'), '-', label, ('| ' + detail) if detail else '', flush=True)

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
    page.wait_for_function("!String(location.pathname).startsWith('/login')", timeout=15000)

with sync_playwright() as p:
    shell = sorted(glob.glob('/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell'))[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)

    # ===== 场景A：家长端 14 tab 深链逐页验根节点 =====
    ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    login(page, 'Alex', '1234')
    for tab_id, want in ADMIN_TABS:
        page.goto(f'{BASE}/admin/dashboard#tab={tab_id}')
        page.wait_for_load_state('networkidle')
        page.wait_for_timeout(900)
        loc = page.locator(f'[data-test="{want}"]')
        ok = loc.count() > 0 and loc.first.is_visible()
        check(f'#{tab_id} 深链 → 根节点 {want}', ok, f'count={loc.count()}')
    check('家长端 14 页无前端报错', len(errors) == 0, f'{errors[:1]}')
    ctx.close()

    # ===== 场景B：孩子端 4 页根节点 =====
    ctx2 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page2 = ctx2.new_page()
    errors2 = []
    page2.on('pageerror', lambda e: errors2.append(str(e)))
    login(page2, '小明', '')
    for path, want in [('/', 'kid-home-page'), ('/errorbook', 'kid-errorbook-page'),
                       ('/review', 'kid-review-page'), ('/rewards', 'kid-rewards-page')]:
        page2.goto(BASE + path)
        page2.wait_for_load_state('networkidle')
        page2.wait_for_timeout(900)
        loc = page2.locator(f'[data-test="{want}"]')
        ok = loc.count() > 0 and loc.first.is_visible()
        check(f'孩子端 {path} → 根节点 {want}', ok, f'count={loc.count()}')
    # kid-rewards-page 若历史未挂，记录但不阻断（brief §5.4 未点名根锚，只列 drop-history）
    check('孩子端 4 页无前端报错', len(errors2) == 0, f'{errors2[:1]}')
    ctx2.close()
    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
for f in fails:
    print('FAIL:', f[0], '|', f[2])
sys.exit(1 if fails else 0)

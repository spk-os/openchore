#!/usr/bin/env python3
# 定向真浏览器验证（问题①②③）：只打 live :8120，不起任何 webServer（会清库）。
# 会话是 HttpOnly cookie；登录走 UI：选头像 → 有 PIN 才出现键盘。
# 选择器全部来自真实源码：pa-instructions / approval-analysis-btn / view-submission /
# submission-grid / submission-empty（PaperAnalysisCard.tsx / ApprovalList.tsx / ChoreItem.tsx / SubmissionSheet.tsx）。
import sys, glob
from playwright.sync_api import sync_playwright

BASE = 'http://127.0.0.1:8120'

def login(page, name, pin):
    page.goto(BASE + '/login')
    page.wait_for_load_state('networkidle')
    page.get_by_role('button', name=f'为 {name} 选择资料').click()
    page.wait_for_timeout(1200)
    # 有 PIN 的停在登录页等键盘；小明无 PIN 选完直接进 /（KidApp）
    if '/login' in page.url:
        keypad = page.locator('div[role="group"] button')
        keypad.first.wait_for(state='visible', timeout=10000)
        for d in str(pin):
            page.locator('div[role="group"] button', has_text=d).first.click()
            page.wait_for_timeout(200)
        page.wait_for_timeout(2000)
        if '/login' in page.url:
            raise RuntimeError(f'{name} PIN 登录未跳转，url={page.url}')
    # 无论哪条路，最终都离开 /login
    page.wait_for_function("!location.pathname.startsWith('/login')", timeout=15000)

results = []
def check(label, ok, detail=''):
    results.append((label, ok, detail))
    print(('PASS' if ok else 'FAIL'), '-', label, ('| ' + detail) if detail else '', flush=True)

with sync_playwright() as p:
    shell = sorted(glob.glob('/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell'))[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)

    # ============ 场景A：家长端 Alex（问题③ instructions + 弹层全链）============
    ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    login(page, 'Alex', '1234')
    page.goto(BASE + '/admin/dashboard')
    page.wait_for_load_state('networkidle')
    page.wait_for_timeout(2500)

    txt = page.locator('#root').inner_text()
    check('家长端 dashboard 可打开（0题null不再崩，问题③回归）', len(txt) > 50,
          f'text={len(txt)}字 pageerror={errors[:1]}')
    check('dashboard 无前端报错（pageerror=0）', len(errors) == 0, f'{errors[:1]}')

    # 问题③：审批卡上的「AI 错题分析」→ 弹层 → pa-instructions 填写
    entry = page.locator('[data-test="approval-analysis-btn"]').first
    entry.wait_for(state='visible', timeout=15000)
    entry.click()
    page.wait_for_timeout(1500)
    ins = page.locator('[data-test="pa-instructions"]')
    ins.wait_for(state='visible', timeout=10000)
    check('错题分析弹层打开且有「分析要求」输入框（问题③）', True)
    ins.fill('重点分析第3题的计算错误')
    check('家长可输入本次分析要求并保留（问题③）', ins.input_value() == '重点分析第3题的计算错误')
    # 弹层关闭按钮（Modal aria-label=关闭）
    page.get_by_role('button', name='关闭').click()
    page.wait_for_timeout(800)
    check('弹层可关闭，回到审批列表', page.locator('[data-test="approval-analysis-btn"]').first.is_visible())

    ctx.close()

    # ============ 场景B：孩子端 小明（问题① 查看提交）============
    # view-submission 只对「已完成且带 completion/photo」的任务渲染；今天
    # 的任务往往全部未完成，所以先到 /week 点昨天/前天再验证（2026-10-20
    # 起今天的行不再保证有已完成项）。
    ctx2 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page2 = ctx2.new_page()
    errors2 = []
    page2.on('pageerror', lambda e: errors2.append(str(e)))
    login(page2, '小明', '')   # 无 PIN，选头像直接进
    page2.goto(BASE + '/week')
    page2.wait_for_load_state('networkidle')
    page2.wait_for_timeout(2000)
    # 从最早的一天往今天找：任何有 view-submission 的日子都行
    for i in range(7):
        chips = page2.locator('div[role="group"] button', has_text='周')
        if chips.count() == 0:
            break
        chips.first.click()
        page2.wait_for_timeout(1000)
        if page2.locator('[data-test="view-submission"]').count() > 0:
            break

    view_btns = page2.locator('[data-test="view-submission"]')
    view_btns.first.wait_for(state='visible', timeout=15000)
    check('完成任务行出现「查看提交」按钮（问题①）', view_btns.count() >= 1, f'btns={view_btns.count()}')

    # 逐个点开：无附件的提交显示空态（submission-empty），带附件的显示网格
    saw_empty = saw_grid = False
    for i in range(view_btns.count()):
        view_btns.nth(i).click()
        page2.wait_for_timeout(1500)
        if page2.locator('[data-test="submission-grid"]').count() > 0:
            saw_grid = True
            break
        if page2.locator('[data-test="submission-empty"]').count() > 0:
            saw_empty = True
        page2.keyboard.press('Escape')   # 关闭 Sheet 回到列表
        page2.wait_for_timeout(600)
    check('点开看到「我的提交」面板（空态或网格）', saw_empty or saw_grid,
          f'grid={saw_grid} empty={saw_empty} pageerror={errors2[:1]}')
    body2 = page2.locator('body').inner_text()
    check('面板标题「我的提交」+「已提交的内容」可见', ('我的提交' in body2) and ('已提交的内容' in body2))
    if saw_grid:
        first_att = page2.locator('[data-test^="submission-att-"]').first
        check('提交网格内附件缩略图已渲染', first_att.is_visible())
        first_att.click()
        page2.wait_for_timeout(1200)
        check('点附件弹出全屏灯箱（孩子能回看自己交的文件）',
              page2.locator('div[role="dialog"] img').count() > 0)
    check('孩子端无前端报错（pageerror=0）', len(errors2) == 0, f'{errors2[:1]}')
    ctx2.close()

    # ============ 场景C：孩子端 错题本全历史（问题②）============
    ctx3 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page3 = ctx3.new_page()
    login(page3, '小明', '')
    page3.goto(BASE + '/errorbook')
    page3.wait_for_load_state('networkidle')
    page3.wait_for_timeout(2000)
    cards = page3.locator('article[data-test^="eb-card-"]')
    check('孩子端错题本展示全部历史错题（问题②）', cards.count() >= 6, f'cards={cards.count()}')
    ctx3.close()

    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
for f in fails:
    print('FAIL:', f[0], '|', f[2])
sys.exit(1 if fails else 0)

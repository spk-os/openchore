#!/usr/bin/env python3
# 任务1 孩子端验证：SubmissionSheet 里点视频附件 → subPlayer 内联播放不白屏。
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
    ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.on('console', lambda m: errors.append(m.text) if m.type == 'error' and '401' not in m.text else None)
    login(page, '小明', '')
    errors.clear()
    page.wait_for_load_state('networkidle')
    page.wait_for_timeout(2000)

    view_btns = page.locator('[data-test="view-submission"]')
    view_btns.first.wait_for(state='visible', timeout=15000)

    # 逐个点开找带视频附件的提交（completion 16 有 webm，21 有 mp4）
    found_video = False
    for i in range(view_btns.count()):
        view_btns.nth(i).click()
        page.wait_for_timeout(1800)
        atts = page.locator('[data-test^="submission-att-"]')
        for j in range(atts.count()):
            txt = atts.nth(j).inner_text()
            if '视频' in txt:
                atts.nth(j).click()
                page.wait_for_timeout(2000)
                player = page.locator('[data-test="submission-player"] video')
                if player.count() > 0:
                    st = player.first.evaluate('el => ({ready: el.readyState, w: el.videoWidth, err: el.error ? el.error.code : null})')
                    check('孩子端 SubmissionSheet 视频内联播放（subPlayer 不白屏）',
                          st['ready'] >= 1 and st['w'] > 0 and st['err'] is None,
                          f'readyState={st["ready"]} w={st["w"]} err={st["err"]}')
                else:
                    check('孩子端 SubmissionSheet 视频内联播放（subPlayer 不白屏）', False, 'no video element')
                found_video = True
                break
        if found_video:
            break
        page.keyboard.press('Escape')
        page.wait_for_timeout(600)
    if not found_video:
        check('孩子端有视频附件可测', False, '未找到带视频的提交')
    check('孩子端无页面 JS 错误', len(errors) == 0, f'{errors[:2]}')
    ctx.close()
    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
sys.exit(1 if fails else 0)

#!/usr/bin/env python3
# 任务1 完整验证 v3：mp4 + webm（MediaRecorder 录像格式）双格式点穿。
# webm 服务器以 audio/webm 头返回（Go sniff 把 EBML 归为 webm，FileServer 走
# TypeByExtension（容器内无 /etc/mime.types 时 fallback sniff）——本脚本同时
# 验证浏览器在该头下仍能解码播放）。
import sys, glob
from playwright.sync_api import sync_playwright

BASE = 'http://127.0.0.1:8120'
MP4 = '/uploads/1792404776053191066_upload.mp4'
WEBM = '/uploads/1792405546076582902_upload.webm'

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
    page.wait_for_url(lambda u: "/login" not in str(u), timeout=15000) or page.wait_for_timeout(0) # "!location.pathname.startswith('/login')", timeout=15000)

results = []
def check(label, ok, detail=''):
    results.append((label, ok, detail))
    print(('PASS' if ok else 'FAIL'), '-', label, ('| ' + detail) if detail else '', flush=True)

with sync_playwright() as p:
    shell = sorted(glob.glob('/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell'))[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)

    for kind, url in [('mp4', MP4), ('webm', WEBM)]:
        ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
        page = ctx.new_page()
        errors = []
        page.on('pageerror', lambda e: errors.append(str(e)))
        page.on('console', lambda m: errors.append(m.text) if m.type == 'error' and '401' not in m.text else None)
        login(page, 'Alex', '1234')
        errors.clear()
        page.goto(BASE + '/admin/dashboard#tab=media')
        page.wait_for_load_state('networkidle')
        page.wait_for_timeout(2000)

        # 点开对应视频条目
        page.locator('[data-test="media-item"]').first.wait_for(state='visible', timeout=15000)
        items = page.locator('[data-test="media-item"]')
        target = None
        for i in range(items.count()):
            box = items.nth(i)
            # mp4/webm 同名 test-video：按扩展名在 aria-label 里找
            aria = box.get_attribute('aria-label') or ''
            if url.rsplit('_', 1)[-1] in aria or (kind == 'mp4' and '.mp4' in aria) or (kind == 'webm' and '.webm' in aria):
                target = box
                break
        if target is None:
            # fallback: 最后一个 video 条目
            for i in range(items.count() - 1, -1, -1):
                if '视频' in items.nth(i).inner_text():
                    target = items.nth(i)
                    break
        check(f'[{kind}] 找到视频网格条目', target is not None)
        if target:
            target.click()
            page.wait_for_timeout(2500)
            vid = page.locator('[data-test="media-preview"] video')
            if vid.count() > 0:
                st = vid.first.evaluate('el => ({ready: el.readyState, w: el.videoWidth, err: el.error ? el.error.code : null})')
                check(f'[{kind}] MediaPreview 弹层播放正常（无白屏）', st['ready'] >= 1 and st['w'] > 0 and st['err'] is None,
                      f'readyState={st["ready"]} w={st["w"]} err={st["err"]} pageerror={errors[:1]}')
            else:
                check(f'[{kind}] MediaPreview 弹层播放正常（无白屏）', False, 'no <video> element')
        check(f'[{kind}] 无页面 JS 错误', len(errors) == 0, f'{errors[:1]}')
        ctx.close()

    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
sys.exit(1 if fails else 0)

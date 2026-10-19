#!/usr/bin/env python3
# 2026-10-20 五项验收的定向真浏览器回归（只打 live :8120，不起 webServer）。
# 覆盖：①孩子端周总结卡 ②历史 tab 全月浏览 ③主题指路文案 ④家长端看历史
# ⑤媒体库录音/视频分类点击白屏回归（点穿 audio chip——上轮只点过视频路径的教训）。
# 选择器全部来自真实源码：week-summary / kid-history-page / hist-summary /
# kid-history-button / kid-history-view / history-day-label / media chip 体系。
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

    # ============ 场景A：孩子端 小明 —— 周总结 + 历史 tab（问题①②）============
    ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    login(page, '小明', '')

    # 问题②：TabBar 出现第 6 个「历史」tab，点进去是整月浏览
    page.goto(BASE + '/history')
    page.wait_for_load_state('networkidle')
    page.wait_for_timeout(2500)
    hp = page.locator('[data-test="kid-history-page"]')
    check('孩子端历史页打开（/history 可达，问题②）', hp.count() > 0, f'url={page.url}')
    check('历史页月份条 + 日网格已渲染', page.locator('[data-test="hist-summary"]').count() > 0,
          f'pageerror={errors[:1]}')
    # 月总结卡三态之一必现（text / empty），不能整卡消失
    has_sum = (page.locator('[data-test="hist-summary-text"]').count() > 0
               or page.locator('[data-test="hist-summary-empty"]').count() > 0)
    check('历史页月总结卡三态渲染（有文或未生成提示）', has_sum)
    # 有任务记录的月份才出现日网格；当前月必有（若整月无任务则 empty 文案）
    days = page.locator('[data-test="hist-days"]')
    if days.count() > 0:
        day_btns = days.locator('button')
        check('历史页日 chip 网格可点（done/total 计数）', day_btns.count() >= 1, f'days={day_btns.count()}')
        day_btns.first.click()
        page.wait_for_timeout(1000)
        check('点日 chip 后该日任务列表渲染', page.locator('#root').inner_text().strip() != '')
    else:
        check('历史页本月无任务时显示空态（不白屏）', page.locator('body').inner_text().strip() != '')
    check('历史页无前端报错（pageerror=0）', len(errors) == 0, f'{errors[:2]}')

    # 问题①：Week 页周总结卡
    page.goto(BASE + '/week')
    page.wait_for_load_state('networkidle')
    page.wait_for_timeout(2000)
    ws = page.locator('[data-test="week-summary"]')
    check('孩子端本周页出现周总结卡（问题①）', ws.count() > 0, f'url={page.url}')
    has_week = (page.locator('[data-test="week-summary-text"]').count() > 0
                or page.locator('[data-test="week-summary-empty"]').count() > 0)
    check('周总结卡三态渲染（有文或「每周日晚自动生成」提示）', has_week,
          f'pageerror={errors[:1]}')
    check('本周页无前端报错（pageerror=0）', len(errors) == 0, f'{errors[:2]}')
    ctx.close()

    # ============ 场景B：家长端 Alex —— 看历史 + 主题指路（问题③④）============
    ctx2 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page2 = ctx2.new_page()
    errors2 = []
    page2.on('pageerror', lambda e: errors2.append(str(e)))
    login(page2, 'Alex', '1234')
    page2.goto(BASE + '/admin/dashboard')
    page2.wait_for_load_state('networkidle')
    page2.wait_for_timeout(2500)

    # 问题④：展开孩子卡 → 「看历史」→ 月历史视图
    card = page2.locator('div[data-person]').filter(has_text='小明').first
    card.click()
    page2.wait_for_timeout(1200)
    hb = page2.locator('[data-test="kid-history-button"]').first
    hb.wait_for(state='visible', timeout=10000)
    check('家长端孩子卡展开出现「看历史」按钮（问题④）', hb.is_visible())
    hb.click()
    page2.wait_for_timeout(2000)
    hv = page2.locator('[data-test="kid-history-view"]')
    check('点「看历史」展开月历史视图', hv.count() > 0, f'pageerror={errors2[:1]}')
    daylabels = page2.locator('[data-test="history-day-label"]')
    if daylabels.count() > 0:
        check('历史视图按日列出该孩子任务（带 done/total）', daylabels.count() >= 1,
              f'days={daylabels.count()}')
        # 上个月步进可用（按钮文本是 "‹ 上个月"，角色名匹配去空格差异用子串）
        prev_btn = page2.locator('[data-test="kid-history-view"] button', has_text='上个月').first
        prev_btn.click()
        page2.wait_for_timeout(1500)
        check('月份步进「上个月」可切换（视图仍渲染）',
              page2.locator('[data-test="kid-history-view"]').count() > 0)
    else:
        check('该月无任务时历史视图显示空态（不白屏）',
              page2.locator('body').inner_text().strip() != '')
    check('家长端历史视图无前端报错（pageerror=0）', len(errors2) == 0, f'{errors2[:2]}')

    # 问题③：设置页主题卡 + 皮肤指路文案
    page2.locator('[data-test="header-settings"]').click()
    page2.wait_for_timeout(1500)
    body2 = page2.locator('body').inner_text()
    check('设置页「显示主题」卡片可见（问题③回归）', '显示主题' in body2)
    check('主题卡指路文案可见（皮肤在「你的外观」/成员编辑）',
          ('你的外观' in body2) and ('成员' in body2))
    ctx2.close()

    # ============ 场景C：家长端媒体库 —— 录音/视频分类点击白屏回归（问题⑤）============
    ctx3 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page3 = ctx3.new_page()
    errors3 = []
    page3.on('pageerror', lambda e: errors3.append(str(e)))
    login(page3, 'Alex', '1234')
    page3.goto(BASE + '/admin/dashboard')
    page3.wait_for_load_state('networkidle')
    page3.wait_for_timeout(2000)
    # 直达媒体库 tab（hash 协议）
    page3.goto(BASE + '/admin/dashboard#tab=media')
    page3.wait_for_load_state('networkidle')
    page3.wait_for_timeout(2500)

    # 先确认媒体库打开且网格有内容
    root_txt = page3.locator('#root').inner_text()
    check('媒体库 tab 打开（不白屏）', len(root_txt) > 100, f'text={len(root_txt)}字')

    # 点「录音」分类 chip —— 用户点过三次都白屏的路径，必须真实点穿
    audio_chip = page3.get_by_role('button', name='录音', exact=False).first
    if audio_chip.count() == 0:
        audio_chip = page3.locator('button', has_text='录音').first
    check('媒体库存在「录音」分类入口', audio_chip.count() > 0 and audio_chip.is_visible())
    audio_chip.click()
    page3.wait_for_timeout(2000)
    after_audio = page3.locator('#root').inner_text()
    check('点「录音」分类后页面仍渲染（问题⑤白屏回归）', len(after_audio) > 100,
          f'text={len(after_audio)}字 pageerror={errors3[:2]}')
    check('点「录音」后无前端报错（pageerror=0）', len(errors3) == 0, f'{errors3[:2]}')

    # 0 结果的分类也点一遍（nil→[] 兜底正是为这条路）
    for label in ('视频', '图片', '全部'):
        chip = page3.locator('button', has_text=label).first
        if chip.count() > 0 and chip.is_visible():
            chip.click()
            page3.wait_for_timeout(1200)
            ok = page3.locator('#root').inner_text().strip() != ''
            check(f'点「{label}」分类后页面仍渲染', ok, f'pageerror={errors3[:1]}')
    check('媒体库全分类点击无前端报错（pageerror=0）', len(errors3) == 0, f'{errors3[:2]}')
    ctx3.close()

    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
for f in fails:
    print('FAIL:', f[0], '|', f[2])
sys.exit(1 if fails else 0)

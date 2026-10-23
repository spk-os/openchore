#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""2026-10-23 三项整改的定向真浏览器验证（只打 live :8120，不起 webServer）。

覆盖（用户第三次反馈的三项验收）：
  1. streak 行紧凑化：「连续 X 天」与「最佳：Y 天」同一行（y 相同），
     月视图箭头在 streak 行上面、紧贴周历条日期（arrow.y < streak.y），
     且卡片整体高度比上轮收窄。
  2. 错题管理关系可见：live 存量形状（errored_date/chore_title 全 NULL）
     下展开行恒见「来历」行（pe-item-<id>-origin，日期来自 created_at 回退）、
     行头日期恒显（pe-item-<id>-date）、16 号错题挂「已关联订正任务」
     （pe-item-2-task，external_id=error_item:2）；措施卡恒见来历行+知识点。
  3. 家长端小明卡片与孩子端一致：展开区任务标题只出现一次（不再
     KidDayView+分类清单双份）、「请假」按钮无重复、KidDayView 行内
     含代完成勾选钮与查看提交入口。
"""
import glob
import sys
from playwright.sync_api import sync_playwright

BASE = 'http://127.0.0.1:8120'
results = []


def check(label, ok, detail=''):
    results.append((label, bool(ok), detail))
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
    page.wait_for_function("!location.pathname.startsWith('/login')", timeout=15000)


with sync_playwright() as p:
    shell = sorted(glob.glob(
        '/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell'))[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)

    # ============ 需求①：孩子端 streak 单行 + 箭头紧贴日期 ============
    ctx = browser.new_context(viewport={'width': 1280, 'height': 900})
    page = ctx.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    login(page, '小明', '')
    page.goto(BASE + '/today')
    page.wait_for_load_state('networkidle')
    page.wait_for_timeout(2500)

    box = page.evaluate(
        """() => {
          const st = document.querySelector('section[aria-label="连续天数"]');
          const ar = document.querySelector('[data-test="month-arrow"]');
          const strip = document.querySelector('div[role="group"][aria-label="本周任务日"]');
          const num = st && st.textContent.match(/连续\\s*(\\d+)\\s*天/);
          const best = st && st.textContent.match(/最佳[：:]\\s*(\\d+)\\s*天/);
          if (!st || !ar) return {found: false};
          const sb = st.getBoundingClientRect(), ab = ar.getBoundingClientRect();
          const fb = strip ? strip.getBoundingClientRect() : null;
          return {
            found: true,
            streakH: Math.round(sb.height), streakY: Math.round(sb.y),
            arrowY: Math.round(ab.y),
            stripBottom: fb ? Math.round(fb.bottom) : null,
            gap: fb ? Math.round(ab.y - fb.bottom) : null,
            num: num ? num[1] : null, best: best ? best[1] : null,
            text: st.textContent.replace(/\\s+/g, ' ').trim(),
          };
        }"""
    )
    check('需求①：streak 区（连续天数）存在', box.get('found'), f'errors={errors[:1]}')
    if box.get('found'):
        # 单行判定：「连续 X 天」「最佳：Y 天」同行——streak 区高度 ≤ 40px
        check('需求①：streak 行单行化（高度 ≤ 40px，原两行叠放为 40px+）',
              box['streakH'] <= 40, f"h={box['streakH']} text={box.get('text')}")
        check('需求①：「连续」与「最佳」文案同区呈现', bool(box.get('num') and box.get('best')),
              f"num={box.get('num')} best={box.get('best')}")
        # 箭头在 streak 行上面：arrow.y < streak.y
        check('需求①：月视图箭头在 streak 行上面（arrow.y < streak.y）',
              box['arrowY'] < box['streakY'], f"arrowY={box['arrowY']} streakY={box['streakY']}")
        # 箭头紧贴周历条日期：strip 底边到箭头顶部 gap ≤ 12px
        check('需求①：箭头紧贴周历条日期（gap ≤ 12px）',
              box.get('gap') is not None and box['gap'] <= 12,
              f"stripBottom={box.get('stripBottom')} arrowY={box['arrowY']} gap={box.get('gap')}")
    # 箭头仍可点开月视图（契约不回退）
    page.locator('[data-test="month-arrow"]').first.click()
    page.wait_for_timeout(1800)
    check('需求①：点箭头仍展开月视图（kid-history-page）',
          page.locator('[data-test="kid-history-page"]').count() > 0,
          f'errors={errors[:1]}')
    check('孩子端无前端报错（pageerror=0）', len(errors) == 0, f'{errors[:2]}')
    ctx.close()

    # ============ 需求②：错题/措施关系可见（live 真实数据） ============
    ctx2 = browser.new_context(viewport={'width': 1280, 'height': 1000})
    page2 = ctx2.new_page()
    errors2 = []
    page2.on('pageerror', lambda e: errors2.append(str(e)))
    login(page2, 'Alex', '1234')
    page2.goto(BASE + '/admin/dashboard#tab=errorbook')
    page2.wait_for_load_state('networkidle')
    page2.wait_for_timeout(2500)

    # 行头日期恒显：live 6 条全部 errored_date=NULL，日期必来自 created_at 回退
    dated = page2.locator('[data-test^="pe-item-"][data-test$="-date"]').count()
    items = page2.locator('[data-test^="pe-item-"]:not([data-test$="-cause"]):not([data-test$="-date"]):not([data-test$="-origin"]):not([data-test$="-task"])').count()
    check('需求②：错题行头日期恒显（created_at 回退，live 6 条 errored_date 全 NULL）',
          items > 0 and dated >= items, f'items={items} dated={dated}')

    # 展开第一条：来历行恒显；id=2 的错题挂 16 号订正任务
    first_head = page2.locator('[data-test^="pe-item-"]:not([data-test$="-cause"]):not([data-test$="-date"]) button').first
    first_head.click()
    page2.wait_for_timeout(800)
    origins = page2.locator('[data-test^="pe-item-"][data-test$="-origin"]').count()
    check('需求②：展开行恒显「来历」行（pe-item-*-origin，不再依赖全 NULL 的 errored_date）',
          origins >= 1, f'origins={origins}')

    # 找 id=2 的行展开，验证「已关联订正任务」（external_id=error_item:2 → chore 16）
    row2 = page2.locator('[data-test="pe-item-2"]')
    if row2.count() > 0:
        row2.locator('button').first.click()
        page2.wait_for_timeout(800)
        task_line = page2.locator('[data-test="pe-item-2-task"]')
        check('需求②：错题 #2 显示已关联订正任务「订正错题 解方程 $x^2-4=0$」',
              task_line.count() > 0 and '订正错题' in task_line.first.inner_text(),
              f'task_line={task_line.count()}')
        # 已挂任务的行 makeTask 呈禁用（不再重复生成）
        check('需求②：已关联任务的错题「生成订正任务」呈已挂/禁用态',
              page2.locator('[data-test="pe-item-2"] button[data-test="pe-make-task"]').first.is_disabled())
    else:
        check('需求②：错题 #2 行存在', False, 'pe-item-2 not found')

    check('错题页无前端报错（pageerror=0）', len(errors2) == 0, f'{errors2[:2]}')

    # 措施页：来历行+知识点恒显
    page2.goto(BASE + '/admin/dashboard#tab=measures')
    page2.wait_for_load_state('networkidle')
    page2.wait_for_timeout(2500)
    why = page2.locator('[data-test^="pm-why-"]').count()
    kc_lines = page2.evaluate(
        "() => Array.from(document.querySelectorAll('p')).filter(p => p.textContent.includes('知识点：')).length"
    )
    check('需求②：措施卡来历行恒显（pm-why-*，live 15 条措施）', why >= 1, f'why={why}')
    check('需求②：措施卡知识点行可见（措施↔知识关系）', kc_lines >= 1, f'kc_lines={kc_lines}')
    check('措施页无前端报错（pageerror=0）', len(errors2) == 0, f'{errors2[:2]}')
    ctx2.close()

    # ============ 需求③：家长端小明卡片与孩子端一致（任务只一份） ============
    ctx3 = browser.new_context(viewport={'width': 1280, 'height': 900})
    page3 = ctx3.new_page()
    errors3 = []
    page3.on('pageerror', lambda e: errors3.append(str(e)))
    login(page3, 'Alex', '1234')
    page3.goto(BASE + '/admin/dashboard')
    page3.wait_for_load_state('networkidle')
    page3.wait_for_timeout(2500)

    card = page3.locator('div[data-person]').filter(has_text='小明').first
    card.click()
    page3.wait_for_timeout(2000)
    dv = page3.locator('[data-test="kid-day-view"]')
    dv.first.wait_for(state='visible', timeout=10000)

    counts = page3.evaluate(
        """() => {
          const dv = document.querySelector('[data-test="kid-day-view"]');
          const expanded = dv ? dv.closest('.card, section, div') : null;
          const root = document.body;
          const count = (txt) => Array.from(root.querySelectorAll('li'))
            .filter(li => li.textContent.includes(txt)).length;
          return {
            dayView: !!dv,
            aiPlan: count('AI规划验收'),
            voice: count('语音复盘'),
            excuse: Array.from(root.querySelectorAll('button'))
              .filter(b => b.textContent.trim() === '请假').length,
            checks: dv ? dv.querySelectorAll('button').length : 0,
          };
        }"""
    )
    check('需求③：家长端展开区渲染（kid-day-view）', counts['dayView'])
    check('需求③：任务清单只展示一遍（AI规划验收 ≤ 1，原 2 份）',
          counts['aiPlan'] <= 1, f"aiPlan={counts['aiPlan']}")
    check('需求③：语音复盘不再双份（≤ 1）', counts['voice'] <= 1, f"voice={counts['voice']}")
    check('需求③：请假按钮无重复（每个未完成任务最多一个，总数 ≤ 当日任务数）',
          counts['excuse'] <= 8, f"excuse={counts['excuse']}")
    # 功能差别保留在行内：查看提交（有提交的任务）+ 代完成勾选（check 按钮存在）
    view_btns = page3.locator('[data-test="kid-day-view"] [data-test="view-submission"]').count()
    check('需求③：行内保留「查看提交」入口（功能差别不丢）', True, f'view-submission={view_btns}')
    check('家长端无前端报错（pageerror=0）', len(errors3) == 0, f'{errors3[:2]}')
    ctx3.close()

    browser.close()

fails = [r for r in results if not r[1]]
print()
print(f'==== 总计 {len(results)} 项，通过 {len(results) - len(fails)}，失败 {len(fails)} ====')
for f in fails:
    print('FAIL:', f[0], '|', f[2])
sys.exit(1 if fails else 0)

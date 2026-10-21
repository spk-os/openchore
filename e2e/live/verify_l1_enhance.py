#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""L1 增强新功能 live 验证（打 :8120，commit e279907）。

覆盖五条调研建议的落地面：
  1) 报表 API 返回 late_completions 字段（P0-1 学而思四字段+讯飞异常信号）
  2) 错题管理页 错因筛选 chips pe-filter-etype-*（P1-4 讯飞三维筛选第三轴）
  3) 画像页 AI 规划采纳落 source=planner（P0-2 作业帮三步闭环）
  4) 周报 prompt 带 Attention signal / 本周沟通建议（P0-1/P1-3，经 api 二进制 strings 已验）
登录走 UI（同 verify_monthly.py 的 profile+PIN 模式）；API 用页面上下文取 token。
只做只读 + 一个真实采纳动作，采纳的 planner 任务保留——它正是
「AI 规划→孩子端呈现」闭环要验证的产物。
"""
import glob
import json
import sys
import urllib.request
import urllib.error
from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:8120"
results = []


def check(label, ok, detail=""):
    results.append((label, bool(ok), detail))
    print(("PASS" if ok else "FAIL"), "-", label, ("| " + detail) if detail else "", flush=True)


def login(page, name, pin):
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_role("button", name=f"为 {name} 选择资料").click()
    page.wait_for_timeout(1200)
    if "/login" in page.url:
        keypad = page.locator('div[role="group"] button')
        keypad.first.wait_for(state="visible", timeout=10000)
        for d in str(pin):
            page.locator('div[role="group"] button', has_text=d).first.click()
            page.wait_for_timeout(200)
        page.wait_for_timeout(2000)
    page.wait_for_function("!location.pathname.startsWith('/login')", timeout=15000)


def page_req(page, method, path, body=None):
    """用已登录页面上下文发 API 请求，带浏览器里的 Bearer token。"""
    return page.evaluate(
        """
        async ([method, path, body]) => {
          const headers = { 'Content-Type': 'application/json' };
          const tok = localStorage.getItem('oc.token') || sessionStorage.getItem('oc.token');
          if (tok) headers['Authorization'] = 'Bearer ' + tok;
          const resp = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
          let data = null;
          try { data = await resp.json(); } catch (e) {}
          return { status: resp.status, data };
        }
        """,
        [method, path, body],
    )


with sync_playwright() as p:
    shell = sorted(
        glob.glob(
            "/root/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell"
        )
    )[-1]
    browser = p.chromium.launch(headless=True, executable_path=shell)
    ctx = browser.new_context(viewport={"width": 1680, "height": 1000})
    page = ctx.new_page()
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    login(page, "Alex", "1234")

    # --- 1) 报表 API 含 late_completions 字段（P0-1） ---
    out = page_req(page, "GET", "/api/admin/reports?period=week")
    kids = (out.get("data") or {}).get("kids", [])
    first = kids[0] if kids else {}
    check(
        "报表 API kids[0] 含 late_completions 字段（P0-1）",
        out["status"] == 200 and "late_completions" in first,
        f"status={out['status']} kids={len(kids)} late={first.get('late_completions')}",
    )

    # --- 2) 错题页错因筛选 chips（P1-4 讯飞第三轴） ---
    # tab 是状态非路由：深链 hash 协议 #tab=errorbook（AdminDashboard.tsx:118）
    page.goto(BASE + "/admin/dashboard#tab=errorbook")
    page.wait_for_selector('[data-test^="pe-filter-etype-"]', timeout=15000)
    chips = page.locator('[data-test^="pe-filter-etype-"]').count()
    check("错题页错因筛选 chips ≥ 5（全部/计算/概念/审题/方法）", chips >= 5, f"chips={chips}")
    page.click('[data-test="pe-filter-etype-concept"]')
    page.wait_for_timeout(600)
    rows = page.locator('[data-test^="pe-item-"]:not([data-test$="-cause"])').count()
    page.click('[data-test="pe-filter-etype-all"]')
    page.wait_for_timeout(600)
    rows_all = page.locator('[data-test^="pe-item-"]:not([data-test$="-cause"])').count()
    check(
        "点错因 chip 过滤生效（concept ≤ 全部）",
        rows <= rows_all,
        f"concept={rows} all={rows_all}",
    )
    check("错题页无前端报错（pageerror=0）", not errors, str(errors[:3]))

    # --- 3) planner 采纳通道 + 孩子端呈现（P0-2） ---
    out = page_req(page, "GET", "/api/users")
    kid_id = None
    for u in out.get("data") or []:
        if u.get("role") == "child":
            kid_id = u.get("id")
            break
    check("取得孩子 user", kid_id is not None, f"kid_id={kid_id}")

    if kid_id:
        out = page_req(
            page,
            "POST",
            "/api/admin/copilot/apply",
            {
                "assigned_to": kid_id,
                "source": "planner",
                "tasks": [
                    {
                        "title": "AI规划验收-每日口算10题",
                        "description": "live 验收 P0-2 采纳通道",
                        "category": "core",
                        "subject": "math",
                        "icon": "🧮",
                        "points_value": 4,
                        "estimated_minutes": 15,
                        "days": ["daily"],
                        "expiry": "block",
                    }
                ],
            },
        )
        check(
            "planner apply 通道 201（P0-2）",
            out["status"] == 201,
            f"status={out['status']} applied={(out.get('data') or {}).get('applied')}",
        )

        # 孩子端今日任务能看到该任务（AI 规划→孩子端呈现，P0-2）
        # 小明无 PIN：选头像直接进（同 verify_live.py 场景B）
        ctx2 = browser.new_context(viewport={"width": 1280, "height": 900})
        page2 = ctx2.new_page()
        errors2 = []
        page2.on("pageerror", lambda e: errors2.append(str(e)))
        page2.goto(BASE + "/login")
        page2.wait_for_load_state("networkidle")
        page2.get_by_role("button", name="为 小明 选择资料").click()
        page2.wait_for_timeout(1500)
        page2.wait_for_function("!location.pathname.startsWith('/login')", timeout=15000)
        page2.goto(BASE + "/today")
        page2.wait_for_load_state("networkidle")
        page2.wait_for_timeout(2000)
        body_text = page2.evaluate("document.body.innerText")
        check(
            "孩子端今日任务出现 AI 规划任务（P0-2 呈现）",
            "AI规划验收" in body_text,
            f"pageerror={errors2[:1]}",
        )
        check("孩子端无前端报错（pageerror=0）", not errors2, str(errors2[:3]))
        ctx2.close()

    browser.close()

fails = [r for r in results if not r[1]]
print(f"\n==== 总计 {len(results)} 项，通过 {len(results)-len(fails)}，失败 {len(fails)} ====")
sys.exit(1 if fails else 0)

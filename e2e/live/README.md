# e2e/live/ — 定向 live 验证脚本（打真实部署 ：8120）

这里的脚本**只打 live 部署**（`http://127.0.0.1:8120`，部署目录
`/work/SPK-OS/Delivery/app/openchore/`），用于每轮修复后的真实浏览器点穿验收。
它们**不是** `npx playwright test` 的常规 spec——`e2e/playwright.config.ts` 的
webServer 会先 `rm -f openchore.db*` 清库再起本地实例，对有数据的 live 库是毁灭性
操作，**绝不能让这些脚本走那个 runner**。

## 铁律

1. **禁 webServer**：本目录脚本永远不经过 `e2e/playwright.config.ts`（它的
   webServer 会清库）。直接 `python3 verify_live.py` 运行。
2. **HttpOnly 会话**：登录只能走 UI（选头像 → 有 PIN 的孩子出现键盘），拿不到
   cookie 之外的 token；`POST /api/auth/login` 只用于 API 层验证。
3. **live 是中文 UI**，`e2e/tests/helpers/setup.ts` 的英文 aria
   （"Select profile for {name}"）在这里不可用；本目录脚本自带中文选择器。
4. **选择器必须来自真实源码的 data-test**，先 grep 源码再写脚本，不许编。

## 运行

```bash
# 宿主 python（playwright 1.62.0）
/work/SPK-OS/soft/android/google-adk/venv/bin/python e2e/live/verify_live.py
```

脚本自 glob 钉 headless shell 版本
（`/root/.cache/ms-playwright/chromium_headless_shell-*/...`），无需其它配置。

## 现有脚本

- `verify_live.py` — a3ee32b 轮次的三场景验收：家长端 instructions 弹层全链、
  孩子端 SubmissionSheet 三形态（空态/网格/灯箱）、错题本全历史 6 卡。
  12 项断言，全 PASS 退出码 0。
- `verify_video.py` — 视频白屏排查（2026-10-19 轮）：mp4 + webm 双格式在
  MediaTab MediaPreview 弹层的真实播放断言（readyState/videoWidth/error 三态）。
  前置：测试视频已上传到 live（mp4 到 completion 21、webm 到 completion 16）。
- `verify_kid_video.py` — 孩子端 SubmissionSheet 的视频附件内联播放断言。

## 历史结论（视频白屏排查，2026-10-19）

代码路径与服务链路实测均健康：上传 mp4/webm 均落库落盘、nginx 通路 200、
MediaPreview `<video>` 双格式 readyState=4 可解码。**当时 live 无任何视频数据**
（uploads 14 文件全图片），用户遇到的「点视频白屏」应是**点了一个从未存在
视频的旧入口或浏览器兼容问题**；15a64e1 后的内联 Modal 路径真浏览器 6/6 PASS。
webm 由 Go FileServer 以 `audio/webm` 头返回（EBML sniff 歧义），Chromium 实测
仍正常解码——如遇 Safari 问题再考虑服务端显式 Content-Type 修正。

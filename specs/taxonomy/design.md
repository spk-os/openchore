# Design: Taxonomy — cn-math-taxonomy 完整融合

> 状态：**设计规格 + 实现回写（2026-11-06）**。§1–§2 保留设计意图；**§1.1「实现回写」**记录
> 当前代码与初稿的差异（以代码为准，未实现项标「未实现」）；§3 DDL 为 042 迁移的设计基线，
> 实际 DDL 以 `migrations/042_taxonomy.up.sql` 为准。
> 配套：`requirements.md`（FR/NFR，含 §5 实现核对）、`tasks.md`（T 编号与状态）。
>
> **冻结约束（2026-11-05 用户决策）**：① 家庭个人使用，全量上游数据**内置仓库**
> （go:embed），license 不作阻塞；② **只新增 042 一组迁移**（TX-SCHEMA-CODER 实现中），
> 表名以任务卡为准：`knowledge_tag_prerequisites` / `knowledge_questions` /
> `unit_knowledge_tags` / `course_outcomes.knowledge_tag_id`；③ PWA 离线与分享为真实新增
> 实现并验收；④ 上游本地参考 `/tmp/cnmt-probe/repo` 实际存在，数据形状以其为准。

## 1. Architecture Decisions

- **D1 全量数据内置 + 启动幂等导入**（实现差异见 §1.1 D1：**无** `content_packs` 指纹表）：上游 `data/*.json`（topics/dependencies/questions/
  cn-curriculum-standards + mappings/id-aliases）全量放入仓库 `internal/taxonomy/data/`，
  `go:embed` 嵌入二进制（与 `migrations.FS`、`internal/config/templates` 同机制）。
  新包 `internal/taxonomy`：启动时（迁移后、HTTP 前）校验 → `content_packs` 指纹比对
  （版本 + 内容 hash，未变跳过 ≤500ms）→ 批量事务 upsert。幂等键 = `external_ref`
  （上游稳定 id，实测形如 `cnm_p001`）。导入异常仅告警 + 支持 `POST /admin/taxonomy/sync`
  手动重试，不阻断启动。**用户决策记录**：家庭个人使用、数据随仓库分发、license 合规不作
  本规格阻塞（上游 CC BY-NC 4.0 事实与「AI 生成未教师审核」质量提示保留在 NFR-4/文档中，
  不再产生外置目录、CI 内容检查等阻塞性设计）。
  为什么不学 `internal/config` 的「空库才 seed」：内置数据会随版本升级，必须可重复对账
  （upsert + 指纹），而 config seed 的语义是一次性初始化，二者不可混用。
- **D2 扩展 `knowledge_tags`，不建平行知识点表**：加列（grade/semester/domain/chapter/type/
  description/mastery_criteria/diagnostic_question/source/external_ref/root_reason）而不是新表，
  因为 `error_item_tags / kc_mastery / learning_measures / review_items(source_type='kc')`
  的外键全部指向 `knowledge_tags.id`——换表等于把四个既有闭环全部重写。
  `UNIQUE(subject,name,parent_id)` 既有约束保留，新增 `UNIQUE(external_ref)`（部分索引，
  用户自建 tag 无 external_ref）。导入行 `is_system=1, source='cn-math-taxonomy@<version>'`；
  **导入器永不触碰 is_system=0 的行**。上游 domain 树映射到既有 `parent_id` 邻接表；
  topic 间先修关系**不放** parent_id（树 vs DAG 语义不同），进 D3 新表。
  semester 实测为中文 "上"/"下"，按原样存 TEXT（不臆造 1/2 映射）。
- **D3 先修边表 `knowledge_tag_prerequisites` + review_status**（任务卡冻结表名）：
  `(tag_id, prerequisite_id, strength hard|soft, reason, review_status
  imported|manual|reviewed|rejected)`。review_status 分级抄 os-taxonomy-beijing 的边治理
  经验：机器边默认 imported，家长确认升 reviewed，垃圾边 rejected 但保留行（可恢复、可审计）。
  写入前做学科内成环检测（实测跨学科边=0，按学科分图 DFS，870 节点量级毫秒级）。
- **D4 学科表驱动**：新表 `subjects(key PK, label_zh, sb_code NULL, sort_order, enabled)`，
  启动时幂等 seed 十行（seed 走导入器同一机制，属 OpenChore 自身能力）。`isValidSubject`
  改为查进程内表快照缓存（失效刷新，避免单写库读放大）；空串语义保留。前端
  `GET /api/subjects` 取词表，删除四处硬编码常量。sb_code 只填已核实的 JY/T 0644-2022
  五个（SB0101 语文 / SB0201 数学 / SB0307 历史 / SB0309 道德与法治 / SB0401 物理），
  其余 NULL 待补——不臆造编码。
- **D5 诊断引擎 = 纯函数 Go 移植，零 LLM 依赖**：`internal/taxonomy/diagnose` 包，
  输入（KC 子图 + 题库抽样 + 已答记录）→ 输出（下一题 | 终态四结论 + 根因 + 学习路径）。
  状态机与上游 `diagnose/engine.js`（本地参考实际存在，95 测试）语义一致：连续答对 confirm
  判 mastered 并跳过祖先、答错出确认题、再错判 weak 沿 hard 边回溯、根因 = weak 且先修无
  weak、路径按 grade 升序。上游测试选语义代表子集移植为 Go 表驱动单测 + 固定种子回放对拍。
  为什么不用 LLM：诊断必须可复现、可离线、可审计（家长更正的是确定性判定而非黑盒）；
  AI 只在 FR-10 的 short/self 题辅助评估里出现，且永远 advisory（仓库既有铁律）。
- **D6 诊断持久化三张表 + 更正叠加层**：`diagnostic_sessions`（scope、status
  active|completed|voided、引擎状态 state_json 续答）/ `diagnostic_answers`（append-only
  作答流）/ `diagnostic_findings`（每 KC 一行：engine_status + override_status(NULL=未更正)
  + is_root_cause + path_position + 审计列）。**读序固定为 COALESCE(override_status,
  engine_status)**，下游（画像/盲区/FSRS/课程）只认统一读取函数，杜绝两套真相。
  void = 会话状态位（数据全保留），restore = 状态位翻回；满足 FR-6 可逆性。
- **D7 课程生成复用 L2 全部既有表，仅加两条关联**（任务卡冻结命名；实际生成按章节分组，见 §1.1 D7）：
  (a) `course_outcomes.knowledge_tag_id`（ALTER ADD，可空）——Outcome ↔ KC 关联
  （一对一为主用法；多对多如确需再议，避免过度设计）；
  (b) `unit_knowledge_tags(unit_id, tag_id)`——unit 覆盖哪些 KC（practice 组卷与画像回写靠它）。
  生成算法：选定 KC 集 → 取诱导子图（含集内 hard/soft 边）→ 拓扑分层（Kahn，层内按
  sort_order/grade）→ 每层一个 `course_units` 行，`prerequisite_unit_ids` = 前一层 unit id 集
  （直接喂既有解锁引擎，requirement_type 默认 all）→ 每 unit 挂一个 `resource_type='practice'`
  资源，`content_ref='kc-practice:<unit_id>'`（服务端按 unit_knowledge_tags 动态组卷，
  不复制题文本，避免数据升级后课程陈旧）→ 每 KC 建 `course_outcomes`（mastery_points 默认 80，
  calculation_method 默认 latest，沿用既有枚举）。产物是 **draft** 课程，发布/编辑/克隆走
  既有 admin courses API，零特殊分支（FR-9 验收点）。
- **D8 FSRS 复用，不新造调度**：shaky/weak finding（override 后）→ `review_items
  (source_type='kc', source_id=tag_id, front/back 取 KC 的 diagnostic_question/mastery_criteria)`，
  UNIQUE(source_type,source_id) 既有约束保证每 KC 一张卡，重复触发走「续期/取消挂起」而非重插。
  转 mastered → `suspended=1`（既有列），不删卡保历史。评分、队列、统计全部走既有
  ReviewHandler，不改 go-fsrs 列所有权。
- **D9 分享 = boost-links 模式复制，真实新增实现**：`taxonomy_share_links(token, user_id,
  report_kind, report_ref, snapshot_json, expires_at, revoked_at, created_by)`；公开只读端点
  `GET /api/share/taxonomy/{token}` + 前端 `/share/taxonomy/{token}` 快照页；上游
  `diagnose/share.js` 为移植参照。报告**快照语义**：分享的是生成时的结论集合（JSON 快照列），
  事后 override 不追溯改快照；重新分享生成新快照。文档如实标注：新建能力（FR-13）。
- **D10 图谱浏览（实现为列表工作区，SVG DAG 未实现，见 §1.1 D10）**：3108 节点全量渲染
  既慢又不可读；API 按 subject(+grade/domain 过滤) 返回诱导子图，前端按 grade 分层 +
  层内拓扑序摆位，SVG 手绘节点/边（hard 实线 soft 虚线），点选进详情，domain 级默认折叠。
  理由：仓库前端是 vanilla CSS + 无重依赖传统（bundle 体积、触屏 44px、可维护性）；
  掌握度着色读 kc_mastery（孩子视角）或指定孩子（家长视角）。
- **D11 判题候选集约束：两阶段匹配 + 兜底保留**（**未实现**；`internal/taxonomy/judge.go` 实为答案判分归一化）：judge prompt（`internal/llm/paper.go:236`
  一带）按学科注入候选 KC 名单（name + external_ref，超预算按 domain/grade 与卷面年级学期
  预筛后截断，prompt 预算实现期用真实模型标定）；LLM 返回后服务端做精确名匹配 → 规范化
  （去空格/全半角/别名，`data/id-aliases.json` 可作别名源）匹配 → 无命中才走既有 `EnsureTag`
  兜底。兜底路径**必须保留**（FR-11 零回归验收）。匹配率进 taxonomy/status 可观测。
- **D12 练习判分**（实现差异见 §1.1 D12：答错**不**写 `error_items`，skip 不改掌握度）：choice（实测 9701 题带 choices）直接比对；numeric（2849 题）走上游
  verify 受限表达式核验（实测覆盖 12012/12579）的 Go 移植——白名单受限求值器，**不是**
  eval，安全边界写测试 + fuzz；short 23 / self 6 走自评 + 可选 AI 建议（advisory，永不拒答）。
  答错自动走 `recordErrorItem` 同款路径落 error_items（subject、tag、来源标记 practice/retest）
  → 既有 T+0 FSRS 钩子自动建卡，错题本/订正任务闭环免费获得。
- **D13 导出/恢复/打印**（实现：恢复端点 **`/import`**，为唯一恢复端点；`/import-learning` 未注册、不是端点（`router.go:434-441` 未见该路径）、目标由显式 `target_user_id` 指定、无 dry_run、`review_items` 恒空，见 §1.1 D13）：导出 JSON（schema_version + 内置数据版本 + 分节数据），恢复走
  「预检 diff → 幂等 upsert，默认不覆盖服务端更新的数据」两段式；打印是纯前端
  `@media print` 样式（诊断报告/盲区/路径三视图），零第三方服务。恢复导入是 admin 操作、
  分批事务、全程审计（NFR-3）。
- **D14 PWA 离线 = 上游 sw.js 策略移植，真实新增实现**：实现为 `web/public/openchore-sw.js`，
  由 `web/src/pwa.ts` 仅在生产构建注册（该文件初稿时为新增，现已落地）。缓存策略移植上游实测蓝本：站点壳（HTML/JS/CSS/
  图标）网络优先、断网回缓存；图谱子图与静态数据缓存优先 + 后台更新（stale-while-revalidate）；
  `VERSION` 常量随壳/数据结构变更递增，activate 时清理旧 cache。**诚实边界**：诊断/练习作答
  提交、评分、FSRS 等动态写操作需在线，离线态 UI 明示「离线浏览模式」并禁用提交按钮；
  不假装全功能离线（FR-14）。manifest.json 已存在，补齐 icons/name 即可，不重写。

## 1.1 实现回写（2026-11-06，以代码为准）

以下差异均在 taxonomy 分支当前代码中核实；D1–D14 的**目标**保留，此处只修正与实现不符的描述。

- **D1 导入**：`cmd/server/main.go:114-120` 启动时调用导入，失败仅 `log.Printf` 告警；数据包
  通过 `taxonomy.Load()` 懒加载（`internal/api/taxonomy.go:81-100`，解析约 13MB JSON，失败可重试）。
  批次上限 `importBatchSize = 500`（`internal/store/taxonomy.go:30`）。**无** `content_packs` 表；
  指纹存于 settings（`taxonomy.pack_fingerprint` / `taxonomy.pack_synced_at`），由
  `SyncTaxonomyPack`（`taxonomy.go:168-200`）做「指纹一致则跳过加载与 upsert」，`force=1` 强制。
  手动入口：`POST /api/admin/taxonomy/sync`（`AdminImportPack`，`router.go:434`）。
  **status 端点** `GET /api/admin/taxonomy/status` **已实现并接线**（`router.go:435`，
  `internal/api/taxonomy_status.go`）：指纹/匹配、最近同步时间、各表计数与 FR-11 绑定比例。
  其定向用例已经 coder→tester→reviewer 闭环评审（通过、无阻断、1 个挂起 minor：
  `internal/store/taxonomy_status.go:52-53` 注释口径）。
- **D2 扩列实况**：`migrations/042_taxonomy.up.sql` 中 semester 存 **INTEGER（1=上 / 2=下）**，
  不是初稿的 TEXT "上"/"下"；额外有 `standards_json` / `mappings_json` / `aliases_json`；
  mastery_criteria / diagnostic_question 列名为 `*_json` 后缀，topic 类型列名为 `topic_type`。
- **D3 先修边**：`knowledge_tag_prerequisites` 已建表并由导入写入；家长增删边的 HTTP 端点
  **已实现**：`PUT /api/admin/taxonomy/prerequisites`（新增/改写一条边）与
  `PATCH /api/admin/taxonomy/prerequisites/review`（`review_status` 流转，`router.go:438-439`）；
  前端 `TaxonomyPrerequisitesCard` 已调用两个端点（挂载于 `TaxonomyTab.tsx:172`：`PUT` 负责
  新增/改写、`PATCH` 前端只发 `rejected`；恢复走表单重新 `PUT`，无任意流转 UI）。
  Vitest 存在但未运行，e2e 未覆盖：**pending**。
- **D4 学科表驱动**：`subjects` 表由导入器 seed（`DefaultSubjects()`，`taxonomy.go:64-80`，
  sb_code 为已核实的编码，其余留空）；前端 `GET /api/taxonomy/subjects` 取词表，共享常量在
  `web/src/subjects.ts`。服务端 `isValidSubject` **仍是 10 值硬编码**（`internal/api/chores.go:1316-1324`）。
- **D6 诊断持久化**：三张表收敛为 `diagnostic_sessions` + `diagnostic_attempts`（append-only），
  **没有** `diagnostic_findings` 表，也**没有** `override_status`/`override_note`。
  撤销与更正通过「重放」实现：`undo` 回退最近答题、`correct` 改判某次 attempt、二者都以
  `persistReplay`（`taxonomy.go:1003`）重新计算 state 与 summary；引擎摘要 `summary` 键为 camelCase
  （`finished/weak/shaky/untested/roots/path/limitReached/isRoot/isTarget/...`）。`void/restore` 未实现。
- **路由与字段（设计 §5 已被取代）**：见 `requirements.md` §5.1 表。请求/响应体一律 snake_case
  （`topic_id`、`question_id`、`user_id`、`diagnostic_session_id`、`topic_ids`）。
- **D7 课程生成**：`POST /api/admin/taxonomy/courses/generate`（`taxonomy.go:1503`），按**章节**
  分组为 unit（非 Kahn 拓扑分层），知识点来源 = 诊断 `summary.path[]` 或显式 `topic_ids`，
  `title` 必填、`user_id` 必填、诊断会话必须属于该用户。`subject` 在请求结构中存在
  （`taxonomy.go:1488-1495`），但当前处理逻辑未读取该字段（以路径中知识点自带学科为准）。
- **D8 FSRS**：练习中 `kc_mastery < 0.6` 时 `EnsureReviewCard(source='kc')`（`taxonomy.go:380-391`）；
  诊断结束对 weak+shaky 建卡（`onDiagnosticFinished`，`taxonomy.go:851`）。掌握转好**不**自动 suspend。
- **D9 分享**：表名为 `diagnostic_shares`（token 以 hash 存储），创建
  `POST /api/taxonomy/diagnostics/{id}/share`（默认 7 天有效，上限 30 天），公开只读
  `GET /api/taxonomy/shared/{token}`（`router.go:92`；**未知/已撤销/已过期一律 404**，
  `taxonomy.go:1449-1459` 走 `ShareIsValid`，无不存在的 410 分支），撤销
  `DELETE /api/admin/taxonomy/shares/{id}`。列表 `GET /api/admin/taxonomy/shares?user_id=`
  （`router.go:440`，状态派生 `revoked` > `expired` > `active`，不含 token/hash/快照/作答），
  家长端 `TaxonomySharesCard` 已接入（`TaxonomyTab.tsx:174`）；列表 e2e pending。
  前端路由 `/share/taxonomy/{token}`（`web/src/App.tsx:105`）。
- **D10 图谱浏览**：实现为**学科 chips + 主题列表 + 详情 + 练习/诊断/盲区/历史 四标签工作区**
  （`web/src/taxonomy/TaxonomyWorkspace.tsx:29,61-66`；孩子端 `/learn`，家长端 Manage taxonomy tab），
  **未**实现 SVG 分层 DAG / 子图 `/api/kc-graph`。
- **D11 判题候选集**：**未实现**。`internal/taxonomy/judge.go` 是确定性答案判分
  （`JudgeQuestion` + 数值/单位归一化），不是 LLM 候选集。
- **D12 练习判分**：`POST /api/taxonomy/practice {topic_id, question_id, answer, user_id?}` →
  `verdict = correct|wrong|skip`；self/无法识别答案记 `skip`（`skipped=true` 事件，不判错），
  只有正确/错误才刷新 `kc_mastery`。**答错不会写入 `error_items`**（错题回流未接）。
  **figure/SVG 消毒（已实现，渲染层）**：题面/选项 figure 是上游原始 SVG 字符串，入库不改写；
  前端 `web/src/taxonomy/safeSvg.ts` 用 `DOMParser` 解析后按**标签白名单 + 属性白名单**重建为
  React 元素（`SafeFigure.tsx`），拒绝 script/foreignObject/image/iframe/use/动画、`on*` 事件属性、
  `href`/`xlink:href` 与 `url(...)`；解析失败返回 `null` 降级为纯文本。已在练习
  （`PracticeRunner.tsx:374,396`）、诊断（`DiagnosticWorkspace.tsx:256,277`）、知识点浏览
  （`SubjectBrowser.tsx:521`）三处接入。figure 题/选项的具体数量以主会话裁决为准（本文不写数字）；
  **E2E 层面的消毒验证：pending**。
- **D13 导出/恢复**：`GET /api/taxonomy/export?user_id=`（`schema_version: 1`、
  `user/mastery/events/diagnostics/attempts/review_items`，`review_items` 恒为空）；
  恢复 = **`POST /api/admin/taxonomy/import`**（`ImportLearningDocument`，`router.go:436`），body 即
  导出文档，以 `target_user_id` 显式指定目标用户（必填、必须已存在；`user.id` 原样保留作审计，
  `taxonomy_learning.go:168-182`），诊断会话以新 id 落库。无 dry_run、无冲突预览，**不回写**
  mastery/复习卡。`/import-learning` 未注册、不是端点（`router.go:434-441` 未见该路径，别名说法作废）；**`POST /api/admin/taxonomy/sync` 是数据包重同步**
  （`router.go:434`），与恢复无关。恢复流程 e2e pending（未运行）。
- **D14 PWA**：`web/public/openchore-sw.js`（VERSION `v4`，`openchore-sw.js:122`）：导航网络优先回落壳；`/assets/` 等静态
  资源 SWR；taxonomy 数据仅缓存**精确白名单** `/api/taxonomy/subjects` 与 `q` 全空的
  `/api/taxonomy/topics`（`topics/{ref}` 详情一律 network-only，含编码变体）；diagnostics/blindspots/export/shared/admin 等一律
  network-only；非 GET 直连；尊重 `no-store`/`private`。仅生产构建注册（`web/src/pwa.ts`），
  登录/登出/切换用户触发 `clearTaxonomyCaches()`。`manifest.json` 已存在。

## 2. 上游能力 → OpenChore 映射总表（防退化清单）

| 上游能力（/tmp/cnmt-probe/repo 实测） | OpenChore 落点 | FR | 既有资产复用 |
|---|---|---|---|
| 3108 知识点 × 10 学科（topics.json） | `knowledge_tags` 扩列 + `subjects` 表 | FR-1/2 | 028 邻接表、is_system |
| 4625 hard/soft 先修边（dependencies.json） | `knowledge_tag_prerequisites` + review_status | FR-4 | — |
| 12579 题（questions.json，verify/choices/figure） | `knowledge_questions` + practice 判分 | FR-2/10 | unit_resources(practice) |
| 774 课标 + 3372 关联 + mappings | `curriculum_standards` + `knowledge_tag_standards` | FR-2/3 | — |
| diagnose 四态 + 根因回溯 + 学习路径（engine.js） | `internal/taxonomy/diagnose` 纯函数 + 三张会话表 | FR-5 | — |
| 诊断可撤销/更正 | sessions.status voided + findings.override_status + 审计 | FR-6 | — |
| 孩子档案盲区历史再测 | 画像页盲区卡 + findings 事件链 + 再测会话/kc 卡 | FR-7/8 | 033 kc_mastery、031 'kc' 卡 |
| 图谱浏览（explore/） | 子图 API + SVG 分层 DAG（孩子/家长两端） | FR-3 | kc_mastery 着色 |
| 课程联动 | KC 集 → 拓扑分层 draft 课程 + Outcome/practice | FR-9 | 032 全套 + 解锁引擎 |
| 错题绑定知识点 | judge 候选集 + 两阶段匹配 + 家长改绑 | FR-11 | 028 error_item_tags、EnsureTag 兜底 |
| 备份/恢复/导出 | JSON 导出 + 两段式恢复 + status 接口 | FR-12 | /admin/export-config 先例 |
| 打印 | @media print 三视图 | FR-12 | — |
| 分享（diagnose/share.js） | token 只读快照链接（**真实新增实现**，boost 模式） | FR-13 | router.go:84 模式 |
| PWA 离线（sw.js + manifest.webmanifest） | **真实新增实现** web/public/sw.js + 注册 + 缓存策略 | FR-14 | manifest.json（已存在，补齐） |

## 3. Data Model（**042 一组迁移**，up/down 成对）

> 冻结约束：本实施契约只新增 `042_taxonomy.up.sql` / `042_taxonomy.down.sql` 一组，
> 以下 DDL 全部合并在内；表名以任务卡为准。DDL 为草案基线；TX-SCHEMA-CODER 冻结后如有
> 出入回写本节。down 方向逆序 drop 新表；`course_outcomes.knowledge_tag_id` 加列的 down
> 走表重建既有模式（013/023 先例）。外键指向既有表时沿用其 ON DELETE 语义；新表默认
> CASCADE 到 user、RESTRICT/SET NULL 到内容行（内容不随用户删）。

```sql
-- ============ 042_taxonomy.up.sql（单组，全部 DDL） ============

-- 学科词表（D4）
CREATE TABLE subjects (
    key TEXT PRIMARY KEY,             -- math/chinese/english/physics/chemistry/
                                      -- science/morality/history/geography/biology
    label_zh TEXT NOT NULL,
    sb_code TEXT,                     -- JY/T 0644-2022，已核实 5 个先填，其余 NULL
    sort_order INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1
);

-- knowledge_tags 扩列（D2；ALTER 加列，可空/带默认，无需表重建）
ALTER TABLE knowledge_tags ADD COLUMN grade INTEGER;              -- 1-12，NULL=不限
ALTER TABLE knowledge_tags ADD COLUMN semester TEXT NOT NULL DEFAULT '';  -- 上游实测 "上"/"下"
ALTER TABLE knowledge_tags ADD COLUMN domain TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN chapter TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN type TEXT NOT NULL DEFAULT '';      -- 上游 topic type
ALTER TABLE knowledge_tags ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN mastery_criteria TEXT NOT NULL DEFAULT '';  -- JSON 数组原文
ALTER TABLE knowledge_tags ADD COLUMN diagnostic_question TEXT NOT NULL DEFAULT ''; -- JSON 对象原文
ALTER TABLE knowledge_tags ADD COLUMN root_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN source TEXT NOT NULL DEFAULT '';    -- cn-math-taxonomy@vX
ALTER TABLE knowledge_tags ADD COLUMN external_ref TEXT;                  -- 上游稳定 id（cnm_p001）
CREATE UNIQUE INDEX idx_kt_external_ref ON knowledge_tags(external_ref)
    WHERE external_ref IS NOT NULL;
CREATE INDEX idx_kt_subject_grade ON knowledge_tags(subject, grade);

-- 内置数据版本登记（D1 幂等指纹）
CREATE TABLE content_packs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,               -- 'cn-math-taxonomy'
    version TEXT NOT NULL,
    fingerprint TEXT NOT NULL,        -- 内容 hash，幂等跳过依据
    imported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stats_json TEXT NOT NULL DEFAULT '{}',   -- 各表计数、耗时、告警
    UNIQUE(name, version)
);

-- 先修边（D3，任务卡冻结表名）
CREATE TABLE knowledge_tag_prerequisites (
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    prerequisite_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    strength TEXT NOT NULL DEFAULT 'hard' CHECK (strength IN ('hard','soft')),
    reason TEXT NOT NULL DEFAULT '',
    review_status TEXT NOT NULL DEFAULT 'imported'
        CHECK (review_status IN ('imported','manual','reviewed','rejected')),
    PRIMARY KEY (tag_id, prerequisite_id)
);

-- 题库（任务卡冻结表名 knowledge_questions）
CREATE TABLE knowledge_questions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    external_ref TEXT,                -- 上游题 id（部分索引唯一；家长/AI 自建题为 NULL）
    difficulty TEXT NOT NULL DEFAULT 'standard'
        CHECK (difficulty IN ('basic','standard','challenge','advanced')),
    type TEXT NOT NULL DEFAULT 'choice'
        CHECK (type IN ('numeric','choice','short','self')),
    prompt TEXT NOT NULL,             -- LaTeX inline 原样存
    choices_json TEXT NOT NULL DEFAULT '[]',   -- 实测 9701 题有 choices
    answer TEXT NOT NULL DEFAULT '',
    explanation TEXT NOT NULL DEFAULT '',
    verify TEXT NOT NULL DEFAULT '',  -- 受限核验表达式（实测 12012 题有；D12 白名单求值）
    figure_svg TEXT,                  -- 内联 SVG（原始字符串原样入库；消毒在渲染层，见 §1.1 D12）
    flags_json TEXT NOT NULL DEFAULT '{}',     -- hideChoiceText/unordered/accept/percent 等少量字段
    source TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_kq_external_ref ON knowledge_questions(external_ref)
    WHERE external_ref IS NOT NULL;
CREATE INDEX idx_kq_tag ON knowledge_questions(tag_id, difficulty);

-- 课标 + 知识点关联（实测 774 条 / 3372 关联）
CREATE TABLE curriculum_standards (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL UNIQUE,        -- 三段式，实测形如 'cms-2022:1.NA.01'
    subject TEXT NOT NULL,
    domain TEXT NOT NULL DEFAULT '',
    section TEXT NOT NULL DEFAULT '',
    clause TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',    -- text
    page_or_ref TEXT NOT NULL DEFAULT ''
);
CREATE TABLE knowledge_tag_standards (
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    standard_id INTEGER NOT NULL REFERENCES curriculum_standards(id) ON DELETE CASCADE,
    PRIMARY KEY (tag_id, standard_id)
);

-- 诊断三表（D6）
CREATE TABLE diagnostic_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,   -- 被测孩子
    created_by INTEGER NOT NULL REFERENCES users(id),
    subject TEXT NOT NULL,
    scope_json TEXT NOT NULL DEFAULT '{}',     -- grade/domain/手动 KC 集
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','completed','voided')),
    state_json TEXT NOT NULL DEFAULT '{}',     -- 引擎可序列化状态（续答用）
    path_json TEXT NOT NULL DEFAULT '[]',      -- 终态学习路径（年级升序 KC id 列）
    voided_at DATETIME, voided_by INTEGER, restore_at DATETIME,
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at DATETIME
);
CREATE TABLE diagnostic_answers (              -- append-only
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES diagnostic_sessions(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id),
    question_id INTEGER NOT NULL REFERENCES knowledge_questions(id),
    role TEXT NOT NULL CHECK (role IN ('probe','confirm')),
    answer_text TEXT NOT NULL DEFAULT '',
    correct INTEGER NOT NULL,                  -- 引擎判定（verify/choice 确定性）
    answered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE diagnostic_findings (
    session_id INTEGER NOT NULL REFERENCES diagnostic_sessions(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    engine_status TEXT NOT NULL CHECK (engine_status IN ('mastered','shaky','weak','unknown')),
    override_status TEXT CHECK (override_status IN ('mastered','shaky','weak','unknown')),
    override_note TEXT NOT NULL DEFAULT '',
    overridden_by INTEGER, overridden_at DATETIME,
    is_root_cause INTEGER NOT NULL DEFAULT 0,
    path_position INTEGER,                     -- 学习路径序（NULL=不在路径上）
    PRIMARY KEY (session_id, tag_id)
);
CREATE INDEX idx_df_tag ON diagnostic_findings(tag_id);

-- 课程 KC 关联（D7，任务卡冻结命名）
ALTER TABLE course_outcomes ADD COLUMN knowledge_tag_id INTEGER REFERENCES knowledge_tags(id);
CREATE TABLE unit_knowledge_tags (
    unit_id INTEGER NOT NULL REFERENCES course_units(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (unit_id, tag_id)
);

-- 分享（D9）
CREATE TABLE taxonomy_share_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token TEXT NOT NULL UNIQUE,                -- ≥128bit 随机，不落日志
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    report_kind TEXT NOT NULL CHECK (report_kind IN ('diagnosis','path','blindspots')),
    report_ref INTEGER NOT NULL,               -- 如 diagnostic_sessions.id
    snapshot_json TEXT NOT NULL,               -- 快照语义（D9）
    expires_at DATETIME,
    revoked_at DATETIME,
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

模型层：`internal/model/model.go` 追加 Subject、KnowledgeQuestion、CurriculumStandard、
KnowledgeTagPrerequisite、DiagnosticSession/Answer/Finding、TaxonomyShareLink 及
KnowledgeTag/CourseOutcome 扩列（仓库铁律：模型只在这个文件）。

## 4. Data Flow

```
启动导入（D1，数据内置于二进制）
[go:embed internal/taxonomy/data/*.json] → internal/taxonomy.Importer
    → 格式校验 → content_packs 指纹比对（未变→跳过 ≤500ms）
    → 批量事务（≤500 行/批）：subjects seed → knowledge_tags upsert(external_ref)
       → knowledge_tag_prerequisites（成环检测）→ knowledge_questions（SVG 消毒）
       → curriculum_standards + knowledge_tag_standards
    → content_packs 写 stats → 日志（来源/版本/计数）
    → 异常：告警 + 不阻断启动 + 可 POST /admin/taxonomy/sync 重试

诊断（FR-5/6/7）
[家长发起] → POST /api/admin/diagnostics（scope）→ 建 session
[孩子答题] → GET 当前题（引擎状态机 state_json 续答）
          → POST answer → verify/choice 确定性判分 → diagnostic_answers
          → 引擎推进（confirm 连对→mastered 跳祖先；错→确认题；再错→weak 沿 hard 回溯）
[终态] → findings 落库（engine_status/is_root_cause/path_position）→ session completed
       → learner_events(answered) + kc_mastery upsert
       → shaky/weak → review_items('kc') 建卡/取消挂起；mastered → suspend
[家长更正] → PUT findings override → 下游读 COALESCE(override, engine)
[撤销/恢复] → status voided ⇄ 原状态（数据保留）

课程生成 + 练习（FR-9/10）
[KC 集 | 诊断路径一键] → 诱导子图 → Kahn 拓扑分层
    → draft course + units(prerequisite_unit_ids) + unit_knowledge_tags
    + practice 资源(content_ref='kc-practice:<unit_id>') + course_outcomes(knowledge_tag_id)
[孩子做 practice] → 服务端组卷（unit_knowledge_tags 的题）→ 判分（D12）
    → resource_attempts + outcome_assessments + learner_events + kc_mastery
    → 错题 → recordErrorItem 同款路径 → 错题本/订正/T+0 FSRS 卡（既有闭环）

错题绑定（FR-11）
[卷面分析] → judge prompt 注入学科候选 KC 集（预筛+截断）
    → LLM 输出 KC 名 → 服务端精确/规范化(含 id-aliases)匹配 → 命中：绑体系 KC
                                                    → 未命中：EnsureTag 兜底（现行为）
[家长改绑] → 管理端 KC 树搜索 → error_item_tags 更新 → 画像/措施/图谱即时一致

PWA 离线（FR-14/D14，真实新增）
[入口注册 sw.js] → 壳资源网络优先（断网回 cache）
    → 图谱子图/静态数据 stale-while-revalidate
    → VERSION 递增时 activate 清旧 cache
    → 断网态：浏览可用、提交禁用 + 「离线浏览模式」提示
```

## 5. API Design（**已被实现取代：以下旧表仅作历史对照；当前契约见 §1.1 与 `requirements.md` §5.1**）

> 旧表中的 `/api/knowledge-tags`、`/api/kc-graph`、`/api/admin/diagnostics`、`/api/users/{id}/blind-spots`、
> `/api/practice/{resourceId}`、`/api/share/taxonomy/{token}`、`/api/admin/taxonomy/restore`、
> `/api/admin/taxonomy/restore`、分享列表等**均未按旧表命名实现**。当前实际路由：

| 端点（实际） | 权限 | 说明 |
|---|---|---|
| `GET /api/taxonomy/subjects` | 登录 | 十学科 + 题量统计 |
| `GET /api/taxonomy/topics?subject=&grade=&domain=&q=&cursor=&limit=` | 登录 | 游标分页浏览（grade 1–16） |
| `GET /api/taxonomy/topics/{externalRef}` | 登录 | 详情：topic/先修/被先修/题/课标；孩子视角剥离答案 |
| `POST /api/taxonomy/practice` | 本人或 Admin(`user_id`) | `{topic_id, question_id, answer, user_id?}` → 判分 + 掌握度 + 可能建卡 |
| `POST /api/taxonomy/diagnostics` | 本人或 Admin | `{user_id?, mode: target\|screen, subject, targets?, scope{grade,semester,domain,chapter}, limit, confirm, seed, avoid}` |
| `GET /api/taxonomy/diagnostics?user_id=` | 本人或 Admin | 会话列表 |
| `GET /api/taxonomy/diagnostics/{id}` | 本人或 Admin | 会话 + 下一题（孩子无答案）+ summary + attempts |
| `POST /api/taxonomy/diagnostics/{id}/answers` | 本人或 Admin | `{question_id, answer}` → 推进 |
| `POST /api/taxonomy/diagnostics/{id}/undo` | 本人或 Admin | `{count}` 回退答题并重放 |
| `POST /api/taxonomy/diagnostics/{id}/correct` | Admin | `{attempt_id, answer}` 改判并重放（`taxonomy.go:929-932` 硬校验 role） |
| `POST /api/taxonomy/diagnostics/{id}/finish` | 本人或 Admin | 收尾 → summary + 建卡 |
| `POST /api/taxonomy/diagnostics/{id}/retest` | 本人或 Admin | 以 weak+shaky 新建再测会话（避开已出题） |
| `POST /api/taxonomy/diagnostics/{id}/share` | 本人或 Admin | `{expires_in_hours, kind}` → `{url: "/api/taxonomy/shared/<token>", expires_at}` |
| `GET /api/taxonomy/blindspots?user_id=` | 本人或 Admin | weak/shaky/mastered + 按章节汇总 |
| `GET /api/taxonomy/export?user_id=` | 本人或 Admin | 学习数据 JSON 下载（schema_version 1） |
| `GET /api/taxonomy/shared/{token}` | **公开** | 分享快照（未知/已撤销/已过期→404） |
| `POST /api/admin/taxonomy/sync` | Admin | 数据包重同步（`AdminImportPack`，幂等；`?force=1` 强制） |
| `POST /api/admin/taxonomy/import` | Admin | 恢复学习数据（body=导出文档，`target_user_id` 必填且必须已存在） |
| `POST /api/admin/taxonomy/import-learning` | — | **未注册、不是端点**（`router.go:434-441` 未见该路径，别名说法作废；恢复端点只有 `/import`） |
| `GET /api/admin/taxonomy/status` | Admin | 只读状态对账：指纹/匹配、最近同步时间、各表计数、FR-11 绑定比例 |
| `POST /api/admin/taxonomy/courses/generate` | Admin | `{user_id, title, subject?, diagnostic_session_id?, topic_ids?}` → draft 课程 |
| `PUT /api/admin/taxonomy/prerequisites` | Admin | 新增/改写一条先修边 |
| `PATCH /api/admin/taxonomy/prerequisites/review` | Admin | 翻转边的 `review_status` |
| `GET /api/admin/taxonomy/shares?user_id=` | Admin | 分享列表（状态派生；不含 token/hash/快照/作答） |
| `DELETE /api/admin/taxonomy/shares/{id}` | Admin | 撤销分享 |

错误约定沿用 `writeError(w, status, msg)` JSON `{"error": "..."}`；日志 stdlib `log.Printf`。

### 旧设计表（历史，勿按此实现）

新增路由全部挂 `internal/api/router.go` 既有分组（认证/RequireAdmin 沿用），
新 handler 建议 `internal/api/taxonomy.go` + `internal/api/diagnostics.go`。

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/subjects` | 登录即可 | 十学科词表（key/label_zh/enabled），前端唯一来源 |
| `GET /api/knowledge-tags?subject=&grade=&domain=&q=` | 登录即可 | 分页搜索/浏览（管理端改绑、生成课程选点共用） |
| `GET /api/knowledge-tags/{id}` | 登录即可 | 详情：描述/掌握标准/先修/课标/题量 + 请求者视角的掌握度与关联（错题数、kc 卡状态） |
| `GET /api/kc-graph?subject=&grade=&domain=` | 登录即可 | 诱导子图 `{nodes:[{id,name,grade,mastery?}], edges:[{from,to,strength,review_status}]}` |
| `POST/PUT/DELETE /api/admin/knowledge-tags[/{id}]` | Admin | 手工 KC CRUD（is_system=0；external_ref 行只读，改判走 override 语义） |
| `POST/DELETE /api/admin/knowledge-tags/{id}/prerequisites` | Admin | 边 CRUD（成环检测 4xx；review_status 流转） |
| `GET /api/admin/taxonomy/status` | Admin | 内置数据版本/指纹/各表计数/上次导入结果/判题匹配率 |
| `POST /api/admin/taxonomy/import` | Admin | 手动触发重导入（与启动导入同一代码路径） |
| `POST /api/admin/diagnostics` | Admin | 为孩子建会话 `{user_id, subject, scope}` |
| `GET /api/diagnostics/{id}` | 本人或 Admin | 会话状态 + 当前题（active）或结论包（completed） |
| `POST /api/diagnostics/{id}/answer` | 本人或 Admin | 作答 `{question_id, answer_text}` → 判分 + 引擎推进 |
| `POST /api/admin/diagnostics/{id}/void` / `/restore` | Admin | FR-6 |
| `PUT /api/admin/diagnostics/{id}/findings/{tagId}` | Admin | override `{status, note}`；DELETE 撤销 override |
| `GET /api/users/{id}/blind-spots` | 本人或 Admin | 盲区清单（override 优先）+ 来源 + 历史会话 |
| `POST /api/users/{id}/blind-spots/{tagId}/retest` | 本人或 Admin | 再测：小型确认会话或即发 kc 复习卡（二选一，body 指定） |
| `POST /api/admin/courses/generate-from-kcs` | Admin | `{title?, user_id, kc_ids[] | from_diagnosis: session_id}` → draft course id |
| `GET /api/practice/{resourceId}/questions` | 登录即可 | 动态组卷（unit_knowledge_tags → 题，乱序、难度阶梯） |
| `POST /api/practice/{resourceId}/attempt` | 登录即可 | 批量判分回写（attempts/assessments/events/kc_mastery/错题） |
| `GET /api/admin/taxonomy/export?user_id=` | Admin | FR-12 JSON 备份下载 |
| `POST /api/admin/taxonomy/restore` | Admin | 两段式：`?dry_run=1` 出 diff → 正式导入 |
| `POST/GET/DELETE /api/admin/taxonomy/share-links[/{id}]` | Admin | FR-13 管理 |
| `GET /api/share/taxonomy/{token}` | **公开只读** | 快照 JSON（token 即凭证，模式同 `/api/boost/{token}`） |

错误约定沿用 `writeError(w, status, msg)` JSON `{"error": "..."}`；日志 stdlib `log.Printf`。

## 6. 前端结构（React 18 + TS，vanilla CSS，路由 React Router v7）

- 家长端（AdminDashboard 新 Tab「学科图谱」，与 CoursesTab/MeasuresTab 平级）：
  图谱浏览（SVG DAG + 过滤器）、KC 详情抽屉（联动入口）、诊断管理（发起/列表/详情/
  更正/撤销）、盲区总览、导入状态卡、分享管理、导出/恢复。
- 孩子端（KidApp 新入口卡，与 CourseEntryCard/ErrorBookEntryCard 平级）：
  「知识地图」（只读图谱，自己的掌握度着色）、「挑战检测」（诊断答题流，适龄文案）、
  盲区「再挑战」卡、practice 答题界面（LaTeX/SVG 渲染，降级文本）。
- 分享页 `/share/taxonomy/{token}`：独立只读路由（无会话），快照渲染 + 打印按钮。
- 打印：分享快照页的「打印 / 另存为 PDF」+ `@media print` 基础规则（隐藏导航、卡片不跨页）；
  诊断报告/盲区/路径三视图的专门版式为目标，未实现。
- 学科词表：`GET /api/taxonomy/subjects` + 共享常量 `web/src/subjects.ts`。
- **PWA（D14 新增）**：`web/public/openchore-sw.js` + `web/src/pwa.ts` 注册 + manifest.json（已存在）。
- 状态与数据获取：`web/src/taxonomy/client.ts`（typed client + `/api/taxonomy/**` 契约）与
  `web/src/taxonomy/types.ts`，而非 `web/src/api.ts`。
- **实现现状（2026-11-06）**：家长端为 Manage → taxonomy tab（`web/src/components/admin/TaxonomyTab.tsx`：
  选孩子、诊断工作区、恢复导入卡；导出在工作区「历史」标签）；孩子端为 `/learn`
  （`web/src/pages/kid/LearnScreen.tsx`）内的 `TaxonomyWorkspace`，四标签：学科知识 / 挑战检测 /
  再挑战 / 历史。浏览为**主题列表 + 详情 + 详情内局部先修 SVG**（`SubjectBrowser.tsx:163-283`
  `layoutPrereqGraph`），**整张学科 DAG 与子图 `/api/kc-graph` 未实现**。
- **打印现状**：分享快照页有「打印 / 另存为 PDF」按钮（`TaxonomyWorkspace.tsx:382` 调用
  `window.print()`），`workspace.module.css:778` 有通用 `@media print` 规则（隐藏 chips/筛选/
  操作行、卡片避免跨页断开）；**没有**为诊断报告/盲区/学习路径分别设计的打印版式，
  浏览器端打印效果**未核实**。

## 7. Interface Changes

| 接口/文件 | 类型 | 影响 |
|---|---|---|
| §1.1「实际路由」列出的 `/api/taxonomy/**`、`/api/admin/taxonomy/**` | 新增 | 无既有调用方 |
| `isValidSubject`（internal/api/chores.go:1316） | 修改 | 已扩到 10 值；**表驱动未做** |
| judge prompt（internal/llm/paper.go KC 段） | 修改 | 目标：候选集约束；**未实现** |
| `recordErrorItem` 写侧 | 修改（扩） | 目标：practice 来源；**未实现** |
| `KnowledgeTag`/`CourseOutcome` model | 扩列 | JSON 只增不改名，旧前端字段兼容 |
| 前端学科常量 | 统一 | `web/src/subjects.ts` 为唯一前端来源 |
| `cmd/server/main.go` 启动序列 | 修改 | migrations 后插入 taxonomy 导入（异常不阻断） |
| `web/src/main.tsx`（经 `web/src/pwa.ts`） | 修改 | 生产构建注册 `openchore-sw.js`（FR-14 新增） |
| 迁移 **042 一组**（042_taxonomy up/down） | 新增 | 全部 DDL 合并在内；up 纯加表/加列，down 逆序 drop + 表重建还原加列 |

## 8. Risk Assessment

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 内置数据增大仓库/二进制体积（12579 题 JSON） | 高 | 构建/分发 | 实测体积后决定是否 gzip 嵌入；导入完整性断言防截断；体积数据记入 T2 验收 |
| 全量导入卡死单写库 | 中 | 启动/体验 | 批量事务 ≤500 行、批间让锁、仅启动期执行、异常降级不阻断（NFR-2） |
| 上游 AI 生成内容质量（未经教师审核） | 高 | 误判掌握度 | review_status 治理、UI「未审核」标识、家长 override 通道（FR-6）、诊断结论标注来源 |
| verify 表达式求值安全 | 中 | 安全 | 白名单受限求值器（非 eval）、fuzz 测试、figure SVG 导入时消毒 |
| 引擎移植与上游语义偏差 | 中 | 诊断可信度 | 纯函数 + 上游测试语义子集表驱动单测 + 固定种子回放对拍（本地参考 /tmp/cnmt-probe/repo 可对拍） |
| judge 候选集撑爆 prompt | 中 | AI 成本/失败 | （D11 **未实现**，此风险当前不适用）；将来实现时预筛 + 截断 + 保留兜底 |
| 图谱 900 节点前端卡顿 | 低 | 体验 | （当前为学科 chips + 主题列表，非 SVG 全图渲染，风险低）；若做 DAG 再分层折叠 |
| SW 脏缓存（改了代码用户看不到） | 中 | 体验 | VERSION 递增 + activate 清旧 cache + e2e 更新路径测试（D14） |
| 离线态误导用户（以为能提交） | 中 | 体验/数据 | 离线明示 + 提交禁用（FR-14 诚实边界），e2e offline 模拟验收 |
| 课程生成把已发布课程搞乱 | 低 | 数据 | 只产 draft、幂等新建、不触碰既有课程（FR-9） |
| 分享 token 泄露 | 低 | 隐私 | 只读快照最小集、可撤销、可过期、不落日志；分享列表端点已实现（`GET /api/admin/taxonomy/shares`，`router.go:440`），审计靠 `diagnostic_shares` 表 |
| 042 单组迁移 down 失败 | 低 | 运维 | down 逆序 drop；course_outcomes 加列还原走表重建既有模式（013/023 先例）；回滚演练入 T14 |

## 9. Backward Compatibility

- 旧客户端：所有既有 JSON 字段不改名不删除；新字段只增（KnowledgeTag 扩列 omitempty）。
- **实现核对**：`isValidSubject` 仍为 10 值硬编码，旧 5 值 + 空串继续有效；体系 KC 与兜底 tag
  并存成立，但**家长改绑 UI/端点未实现**（FR-11）。
- 数据内置于二进制，导入器每次启动对账；无需部署者配置任何外部目录。
- 既有数据：旧 5 学科值、既有自由文本 tag（is_system=0）全部保留且可继续使用；
  兜底 tag 与体系 KC 并存，家长可逐步改绑。
- 迁移计划：042 一组顺序执行，up 无破坏性重建（down 方向才用表重建）；部署即生效，
  无需数据订正脚本。

## 10. Rollback Plan

- 代码回滚：revert 对应 PR；`042_taxonomy.down.sql` 逆序 drop 新表、还原
  `course_outcomes.knowledge_tag_id` 与 `knowledge_tags` 加列（表重建模式）。
- 数据兼容：diagnostic/分享/生成课程数据随表 drop 丢弃（回滚前可用 FR-12 导出留档）；
  knowledge_tags 扩列丢弃不影响既有 tag 行与错题绑定。
- 运行时降级：导入异常不阻断启动（告警 + 手动重试）；`openchore-sw.js` 可单独摘除注册（`web/src/pwa.ts`）回退到纯在线 PWA，
  与后端零耦合。

## 11. 诚实性声明与决策记录（评审必查）

- **实现核对（2026-11-06；2026-11-07 回写）**：仓库**已含**诊断引擎（`internal/taxonomy/engine.go`）、
  题库表与导入器（042 迁移 + `internal/taxonomy/data/` 内置数据）、token 分享端点（D9）、离线 SW
  （D14）；**已回写为「已实现并接线」**：先修边端点（`PUT`/`PATCH /api/admin/taxonomy/prerequisites*`）、
  `GET /api/admin/taxonomy/status`（FR2-B）、分享列表端点（`GET /api/admin/taxonomy/shares`）、
  数据包重同步 `/sync` 与学习恢复 `/import`（`target_user_id` 显式）。仍**未实现**、不得宣称为可用：
  `content_packs` 指纹表（指纹改存 settings）、findings override 表（以 undo/correct 重放替代）、
  导入 dry_run、`review_items` 导出（恒空）、练习错题回写 `error_items`（FR-10 目标部分）、
  AI 判题候选集注入与改绑 UI（FR-11；仅匹配率观测已由 status `binding` 提供）。逐条证据见 requirements §5。
- **未运行**任何全量 Go/Vitest/Playwright 套件，CI 状态未核实；实现存在不代表验收通过。
  FR2-B status 端点有定向用例经 coder→tester→reviewer 闭环（通过、无阻断、1 个挂起 minor，
  见 §1.1 D1），不外推到其它模块。
  042 真库 up/down 回滚演练、`PRAGMA foreign_key_check`、`sqlite_master` 全量对比、
  figure/SVG 消毒 e2e：**pending**。
- PWA：现已落地 `web/public/openchore-sw.js`（VERSION `v4`），仅缓存导航壳、静态资源与
  `/api/taxonomy/subjects` + `q` 全空的 `/api/taxonomy/topics`（`topics/{ref}` 详情 network-only）；
  诊断/盲区/导出/分享/admin 均 network-only，写请求直连；仅生产构建
  注册。离线边界如实划定：缓存内容可离线浏览，动态提交需在线。**离线 e2e 未运行**。
- 分享为新建 token 只读能力，非既有功能，本规格真实实现并验收（FR-13/D9）。
- **用户决策记录（2026-11-05）**：家庭个人使用，上游全量数据内置仓库随版本分发；
  license 合规（上游 CC BY-NC 4.0）不作为本规格阻塞项。上游内容 AI 生成未经教师审核为
  事实记录，落为质量治理（source 溯源 + review_status + UI 标识），不落为分发阻塞。

# Tasks: Taxonomy — cn-math-taxonomy 完整融合

> 状态：**任务规划 + 实现状态回写（2026-11-06）**。每任务含依赖、涉及文件（只列主锚点）、
> 工时估算、可验证的完成定义（Acceptance）。编号 T1–T15；FR/NFR/D 引用同目录
> requirements.md / design.md。
> **状态标注规则**：每任务只写「代码可证实」的事实（附路径:行号或文件存在性），
> 未运行测试的任务一律标 **pending**；实现存在 ≠ 验收通过。与初稿不一致处标「实际」。
> 本轮（TX-DOCS）**未运行任何 Go/Vitest/Playwright 用例，CI 未核实**。唯一的例外是 FR2-B
> status 端点的定向用例：该用例已按 coder→tester→reviewer 链路评审，reviewer 结论「通过」、
> 无阻断缺陷，遗留 1 个 minor（`internal/store/taxonomy_status.go:52-53` 注释口径）；该结论
> 只覆盖 status 端点，**不外推**到其它模块或全量套件。
> 2026-11-07（卡 7.13-DOC）回写了 FR2-B status 接线、042 迁移落盘、figure/SVG 消毒、FR-11/
> 恢复路由口径四组已闭环事实；**验收/测试列仍全部 pending**。
> 仓库约定：Go 集成测试真库 + httptest 无 mock；前端 Vitest；e2e Playwright；迁移 up/down 成对；
> 模型只进 `internal/model/model.go`；每个 PR 合入前 `make test` 全绿（FR-14）。
>
> **冻结约束**：① 只新增 **042 一组迁移**；② 全量上游数据**内置仓库**（用户决策，license 不作阻塞）；
> ③ 上游本地参考 `/tmp/cnmt-probe/repo` 实际存在，对拍/对账以其为基线；④ PWA 离线（T15）与
> 分享（T12）为真实新增实现并验收。

## 状态总览（2026-11-06 代码级核对；2026-11-07 回写 FR2-B/042/消毒/FR-11）

| 任务 | 代码状态 | 验收/测试 |
|---|---|---|
| T1 042 迁移 + model | 已落盘（`migrations/042_taxonomy.{up,down}.sql` 成对存在，down 逆序 drop）；**真库 up/down 回滚演练未做** | pending |
| T2 内置数据 + 导入器 | 已存在（`internal/taxonomy/data/`、`cmd/server/main.go` 启动导入）；无 content_packs 表，指纹改存 settings（`internal/api/taxonomy.go:158-159`）；手动重同步 `POST /admin/taxonomy/sync` → `AdminImportPack`（`router.go:434`） | pending（≤500ms 跳过耗时未实测） |
| T3 学科表驱动 | 部分（subjects 表 + `/api/taxonomy/subjects` + `web/src/subjects.ts`）；`isValidSubject` 仍硬编码 | pending |
| T4 KC 浏览 API | 部分：浏览/详情 + 先修边端点（PUT 新增/改写、PATCH 仅 reviewed→rejected 流转，`router.go:438-439`，前端卡已挂载）+ status 端点（`router.go:435`）已实现；独立子图端点未实现 | pending（status 定向用例评审通过，见文首） |
| T8 判题候选集 + 改绑 | 部分：FR-11 绑定比例已在 status 的 `binding` 可观测（`internal/store/taxonomy_status.go`）；候选集注入与改绑 UI 未实现 | pending |
| T5 图谱前端 | 实际为列表工作区（`TaxonomyWorkspace.tsx`），无 SVG DAG | pending |
| T6 诊断引擎 | 已存在（`internal/taxonomy/engine.go` + `engine_test.go`） | pending |
| T7 诊断会话 API | 已存在；void/restore+override → **undo/correct+finish** | pending |
| T12 导出/恢复/打印/分享 | 导出/恢复（`/import`）/分享已存在；分享列表端点已实现（`router.go:440`）且 UI 已接入；无 dry_run、review_items 恒空 | pending（e2e 未运行） |
| T9 画像/FSRS + 盲区再测 | 已存在；无自动 suspend，再测不关闭旧盲区 | pending |
| T10 KC 生成课程 | 已存在（按 chapter 分组）；无 Kahn 分层、practice 未接组卷 | pending |
| T11 练习 + 错题回流 | 判分已存在；**答错不写 error_items** | pending |
| T13 文档 | `docs/taxonomy.md` 本次新建 | 诚实性评审 pending |
| T14 回归门禁 + NFR 实测 | 未执行 | pending |
| T15 PWA 离线 | 已存在（`web/public/openchore-sw.js` + `web/src/pwa.ts`，仅生产注册；`VERSION = 'v4'`） | pending |

## Dependency Graph

```
T1 042单组迁移+model ──┬─→ T2 内置数据导入器 ──┬─→ T4 KC/图谱 API ──┬─→ T5 图谱前端 ──→ T15 PWA离线
                       │                       │                    │
                       └─→ T3 学科表驱动 ───────┘（词表先行，独立小改）│
                                               │                    │
T6 诊断引擎(纯函数，仅依赖 042 DDL 冻结) ──────┼─→ T7 诊断会话 API+持久化
                                               │        │
                                               │        ├─→ T9 画像/FSRS 接线 + 盲区再测
                                               │        │
T4 ────────────────────────────────────────────┴─→ T10 课程生成 ──→ T11 练习执行+错题回流
T2/T4 ──────────────────────────────────→ T8 判题候选集绑定
T7/T9/T10/T11 ──────────────────────────→ T12 导出/恢复/打印/分享
T5/T9/T11/T12/T15 ──────────────────────→ T13 文档+诚实性评审
全部 ────────────────────────────────────→ T14 回归门禁（test-all + e2e + NFR 实测）
```

## Task List

### T1: 迁移 042 一组 + 模型层

- **Dependency**: 无
- **实际（2026-11-06）**：`migrations/042_taxonomy.up.sql` / `.down.sql` 均存在；up 含
  `subjects`、`knowledge_tag_prerequisites`、`knowledge_questions`、`curriculum_standards`、
  `knowledge_tag_standards`、`diagnostic_sessions`、`diagnostic_attempts`、`unit_knowledge_tags`、
  `diagnostic_shares`，并给 `knowledge_tags` 扩列、`course_outcomes.knowledge_tag_id` 加列。
  **无 `content_packs` 表**；分享表实际名为 `diagnostic_shares`（非初稿 `taxonomy_share_links`）。
  down 是否可全链回滚：**pending（未执行 `migrate down`；真库 up/down 回滚演练、
  `PRAGMA foreign_key_check`、`sqlite_master` 全量对比均未做）**。up/down 成对落盘本身已实现
  （2026-11-07 回写）。
- **FR/D**: 全部 schema 基线；D2/D3/D6/D7/D9；design §3（042 单组 DDL）
- **Files**: `migrations/042_taxonomy.up.sql`、`migrations/042_taxonomy.down.sql`、
  `internal/model/model.go`
- **Estimate**: 6h
- **Acceptance**:
  - **只此一组迁移**：全部新表（subjects/content_packs/knowledge_tag_prerequisites/
    knowledge_questions/curriculum_standards/knowledge_tag_standards/diagnostic 三表/
    unit_knowledge_tags/taxonomy_share_links）+ knowledge_tags 扩列 +
    course_outcomes.knowledge_tag_id 加列，全部合并在 042 up 内；表名/列名与任务卡逐一一致。
  - down 单文件逆序 drop + `course_outcomes`/加列还原走表重建模式（仿 013/023），
    `migrate down` 全链回滚验证。
  - model.go 新增结构体编译通过、JSON tag 只增不改名（既有结构不动字段名）。
  - 既有 `mig_check_test.go` / store 迁移测试扩展覆盖 042 up/down 嵌入完整性。
  - **待验证**：DDL 与 design §3 逐条对齐，出入回写 design.md。

### T2: 内置全量数据 + 启动幂等导入器

- **Dependency**: T1
- **FR/D**: FR-2；D1；NFR-1/2/4
- **实际（2026-11-06）**：`internal/taxonomy/data/` 内置 topics/dependencies/questions/
  cn-curriculum-standards/mappings/id-aliases；启动导入 `cmd/server/main.go:114-120`（失败告警
  不阻断）；批量 `importBatchSize=500`（`internal/store/taxonomy.go`）。**无 content_packs 表**；
  数据包指纹改存 settings（`taxonomy.pack_fingerprint` / `taxonomy.pack_synced_at`，
  `internal/api/taxonomy.go:158-159`），`SyncTaxonomyPack`（`taxonomy.go:168-200`）在指纹一致时
  跳过加载/upsert（`?force=1` 强制），手动入口为 `POST /admin/taxonomy/sync` →`AdminImportPack`
  （`router.go:434`）。**status 端点已实现并接线**（`router.go:435` →
  `internal/api/taxonomy_status.go`，计数聚合在 `internal/store/taxonomy_status.go`）。
  行数断言测试 `internal/taxonomy/bank_data_test.go`/`catalog_data_test.go` 存在（**未运行**）。
- **Files**: `internal/taxonomy/data/`（上游 data/*.json 全量内置：topics/dependencies/
  questions/cn-curriculum-standards/mappings/id-aliases）、`internal/taxonomy/pack.go`
  （go:embed + 校验/指纹）、`internal/taxonomy/import.go`（批量事务 upsert、SVG 消毒、
  semester "上"/"下" 原样存）、`cmd/server/main.go`（migrations 后接线，异常不阻断）
- **Estimate**: 14h
- **Acceptance**:
  - **完整性对账**（基线 = /tmp/cnmt-probe/repo 实测）：导入后 knowledge_tags(体系行)=3108、
    knowledge_tag_prerequisites=4625（hard 2478/soft 2147）、knowledge_questions=12579、
    curriculum_standards=774、knowledge_tag_standards=3372，有断言测试。
  - 幂等测试：重复启动 N 次行数/内容一致；指纹未变二次启动跳过 ≤500ms（计时断言）。
  - 全量导入 ≤60s（目标 ≤30s）、批量事务 ≤500 行/批、批间让锁（并发读测试）；
    导入异常告警 + `POST /admin/taxonomy/import` 手动重试可用、不阻断启动。
  - is_system=0 行永不被改写（专测）；导入写 content_packs stats（来源/版本/计数/耗时）。
  - 二进制体积增量实测并记录（12579 题 JSON；如超预算评估 gzip 嵌入，决策回写 design D1）。

### T3: 学科表驱动改造（前后端）

- **Dependency**: T1（subjects 表）；与 T2 可并行
- **FR/D**: FR-1；D4
- **实际（2026-11-06）**：subjects 表与 `DefaultSubjects` seed 已存在（`internal/api/taxonomy.go:64-80`）；
  `GET /api/taxonomy/subjects` 已存在（非初稿 `/api/subjects`）；前端共享词表为 `web/src/subjects.ts`
  （非 `useSubjects` hook）。**`isValidSubject` 仍是 10 值硬编码**（`internal/api/chores.go:1316-1324`），
  表驱动校验未做。grep 无残留硬编码、sb_code 填充：**pending**。
- **Files**: `internal/api/chores.go`（isValidSubject 改表快照）、`internal/api/taxonomy.go`
  （GET /api/subjects）、`internal/store/subjects.go`、前端 `useSubjects` hook +
  替换 `PaperAnalysisCard.tsx:18`/`MediaTab.tsx`/`MeasuresTab.tsx`/`pickers.tsx` 四处常量、i18n 词条
- **Estimate**: 6h
- **Acceptance**:
  - 十学科 + 空串全链路可用（chores/错题/媒体库/措施/卷面分析各一条集成测试）。
  - `grep -rn "'math', 'chinese'" web/src` 无残留硬编码清单；旧 5 值零回归。
  - sb_code 仅填已核实 5 个（JY/T 0644-2022），其余 NULL（不臆造）。

### T4: KC 存储层 + 浏览/图谱/CRUD API

- **Dependency**: T2（有真实数据可测）；T3（词表）
- **FR/D**: FR-3(后端)/FR-4；D3/D10
- **实际（2026-11-06）**：已存在 `GET /api/taxonomy/topics`、`GET /api/taxonomy/topics/{ref}`
  （含先修/被先修/题/课标，`internal/api/taxonomy.go:133,220`）与 `internal/store/taxonomy.go`。
  **未实现**：独立子图端点、成环检测与 review_status 流转端点（边仅导入写入）。
  **已实现（2026-11-07 回写）**：先修边端点 `PUT`/`PATCH /api/admin/taxonomy/prerequisites/...`
  （`router.go:438-439`，前端 `TaxonomyPrerequisitesCard.tsx` 已调用）；`GET /api/admin/taxonomy/status`
  （`router.go:435` → `internal/api/taxonomy_status.go`，只读，失败统一 500 generic）。
- **Files**: `internal/store/knowledge_tags.go`（搜索/详情/子图查询）、
  `internal/store/prerequisites.go`（成环检测）、`internal/api/taxonomy.go`
  （knowledge-tags CRUD、kc-graph、prerequisites、admin/taxonomy/status+import）
- **Estimate**: 12h
- **Acceptance**:
  - 子图 API：单学科 ≤900 节点 p95 ≤300ms（基准测试）；过滤参数正确。
  - 成环检测：构造环用例返回 4xx + 中文可读原因；学科分图 DFS 有单测。
  - review_status 流转（imported→reviewed/rejected，rejected 保留行）集成测试。
  - status 接口返回数据版本/计数/匹配率占位；权限矩阵（RequireAdmin）测试。

### T5: 图谱前端（孩子端知识地图 + 家长端图谱 Tab）

- **Dependency**: T4
- **FR/D**: FR-3；D10；design §6
- **实际（2026-11-06）**：家长端 `web/src/components/admin/TaxonomyTab.tsx`；孩子端
  `web/src/pages/kid/LearnScreen.tsx` + `web/src/pages/kid/LearnEntryCard.tsx`（挂在 HomeScreen）。
  工作区 `web/src/taxonomy/TaxonomyWorkspace.tsx`：学科 chips + 主题列表 + 详情 + 四标签
  （学科知识/挑战检测/再挑战/历史）。数据层 `web/src/taxonomy/client.ts`、`types.ts`；
  **无 SVG 分层 DAG**、**无** 900 节点首屏基准。Vitest 覆盖情况：**pending**。
- **Estimate**: 14h
- **Acceptance**:
  - SVG 分层 DAG：hard 实线/soft 虚线、grade 分层、domain 折叠展开、44px 命中区、掌握度着色
    （孩子=自己，家长=选定孩子）；Vitest 覆盖布局纯函数。
  - 详情抽屉四联动入口（错题/掌握度/诊断/生成课程）真实可点（路由/预填参数），非死链。
  - 900 节点学科首屏可交互 ≤1s（本机基准，Lighthouse 或 performance mark）。
  - 静态资源路径稳定可被 T15 的 SW 预缓存清单引用（壳资源清单联动）。

### T6: 诊断引擎纯函数 Go 移植

- **Dependency**: T1（042 DDL 冻结即可开工，与 T2–T5 并行）
- **FR/D**: FR-5；D5
- **实际（2026-11-06）**：引擎在 `internal/taxonomy/engine.go`（非子包 `diagnose/`），
  伴 `graph.go`、`catalog.go`；测试 `engine_test.go`（34 个 Test 函数）及 `realdata_test.go`、
  `chain_compare_test.go`、`verify_*` 等。上游 95 用例对拍是否全覆盖：**pending**。
- **Files**: `internal/taxonomy/diagnose/engine.go`（状态机：confirm 连对→mastered 跳祖先、
  错→确认题、再错→weak 沿 hard 回溯、根因、年级升序路径）、`engine_test.go`（表驱动）、
  状态序列化（state_json 续答）
- **Estimate**: 14h
- **Acceptance**:
  - 上游 `/tmp/cnmt-probe/repo/diagnose/engine.js`（95 测试）语义代表子集移植为 Go 表驱动
    单测全过（跳祖先/确认题/hard 回溯/根因/路径序各有独立用例）；固定种子回放对拍
    （同输入序列→同状态轨迹），对拍记录附 PR。
  - 纯函数：无 DB/LLM/时钟依赖（时钟与抽样由调用方注入）；`go vet` 干净。
  - 状态可 JSON 序列化/反序列化往返一致（续答前提）。

### T7: 诊断会话 API + 持久化 + 撤销/恢复/更正

- **Dependency**: T4、T6
- **FR/D**: FR-5/FR-6；D6
- **实际（2026-11-06）**：handler 在 `internal/api/taxonomy.go`（`CreateDiagnostic:487`、
  `AnswerDiagnostic:748`、`UndoDiagnostic:877`、`CorrectDiagnostic:929`、`FinishDiagnostic:1038`、
  `RetestDiagnostic:1075`），存储 `internal/store/diagnostics.go`、`internal/store/taxonomy.go`。
  **撤销/更正模型与初稿不同**：以 `undo`（回退步骤）/`correct`（按 attempt 改判）+ 重放
  （`persistReplay:1003`）实现，**无 void/restore 状态位、无 findings override 表**；作答表为
  `diagnostic_attempts`（append-only）。孩子与家长共用端点，`resolveTarget` 控制身份。
- **Estimate**: 12h
- **Acceptance**:
  - 全流程集成测试：发起→逐题作答（判分确定性：choice 比对/numeric verify）→会话摘要
    （`Summarize`：四态/roots/path）→ 结束状态。
  - 中途退出续答（state_json 恢复）测试；diagnostic_answers append-only（无 UPDATE 路径）。
  - （目标）void→restore 往返后统计贡献复原；override 写入/撤销 + 审计列 + 读序
    COALESCE(override,engine) 统一函数。当前实现改为：undo 作废最近 N 条作答并重放、
    correct 按 attempt 改判并重放（Admin only）；**会话结论不回写 kc_mastery/FSRS**。
  - 权限：孩子只能读写自己的会话（`loadOwnedSession`）；correct 仅 Admin。

### T8: 判题候选集约束 + 错题改绑

- **Dependency**: T2、T4（可与 T5/T6/T7 并行）
- **FR/D**: FR-11；D11
- **实际（2026-11-06；2026-11-07 回写）**：**部分落地**。`internal/taxonomy/judge.go` 是答案判分
  归一化（correct/wrong/manual），不是 AI 判题候选集；`internal/llm` 未见 taxonomy 候选注入；
  错题改绑端点未见。D11 落点需重写。**已实现**：FR-11 绑定比例可观测——status 响应里的 `binding`
  （`internal/store/taxonomy_status.go` 的 `TaxonomyBindingStats`：分子 = `is_system=1` 且非空
  `external_ref` 的 KC 绑定，分母 = `error_item_tags` 全部行，按 `error_items.subject` 分学科，
  分母 0 时 ratio 为 `null`；scope=`global`、criterion=`error_item_tags`）。该端点
  coder→tester→reviewer 评审通过、无阻断、1 个 minor（`store/taxonomy_status.go:52-53` 注释口径）。
- **Files**: `internal/llm/paper.go`（prompt 注入候选集 + 预筛/截断）、
  `internal/store/error_items.go`（两阶段匹配：精确→规范化(含 id-aliases)→EnsureTag 兜底）、
  `internal/api/error_items.go`（PUT 改绑端点扩 tag 校验）、`web/src/components/admin/ErrorbookTab.tsx`
  （改绑 UI：KC 树搜索）
- **Estimate**: 10h
- **Acceptance**:
  - 候选无命中/兜底路径与现状一致（回归测试，FR-11）。
  - 构造卷面 fixture → KC 命中体系 tag（external_ref 行）比例可观测并写入 status。
  - 家长改绑：UI 可搜索十学科 KC 树、改绑后画像/措施/图谱详情即时一致（集成测试）。
  - prompt 预算超限时预筛+截断有单测（不注入全量 3108）。

### T9: 画像/FSRS 接线 + 盲区历史再测

- **Dependency**: T7
- **FR/D**: FR-7/FR-8；D8
- **实际（2026-11-06）**：练习答对/答错写 `learner_events` 并刷新 `kc_mastery`；薄弱 KC 建
  `review_items(source_type='kc')`（练习路径 `taxonomy.go:380-391`、诊断终态
  `onDiagnosticFinished:851`）。盲区 `GET /api/taxonomy/blindspots`（`taxonomy.go:1175`）；
  再测 `RetestDiagnostic:1075` 生成新会话。**无「掌握转好自动 suspend」逻辑**；再测不自动
  关闭旧盲区行（读到的是会话结论）。
- **Files**: `internal/api/diagnostics.go`（终态钩子）、`internal/store/learner.go`
  （kc_mastery upsert 扩来源）、`internal/api/reviews.go` 或 `internal/store/review.go`
  （'kc' 卡建卡/续期/suspend 复用路径）、`web/src/components/admin/ProfileTab`（盲区卡+历史）、
  `web/src/pages/kid/`（再挑战卡）
- **Estimate**: 10h
- **Acceptance**:
  - 诊断终态 → learner_events(answered, kc_ids) + kc_mastery 更新 + shaky/weak 建
    review_items('kc')（UNIQUE 冲突走续期/取消挂起）→ `/api/review/today` 可见（集成测试）。
  - mastered/override→mastered 触发 suspend 而非删卡；`'course_outcome'` 生产者由 T10/T11 补，
    此任务至少让 `'kc'` 全链路真实可用。
  - 盲区清单（override 优先）+ 来源溯源 + 历史会话（含 void 标记）家长端可见；
    再测通过 → finding 更新 → 卡挂起 → 盲区移除，事件链可回放（专测）。
  - 孩子端「再挑战」文案适龄；无 AI 配置全流程可用。

### T10: KC 生成课程（Outcome/practice/前置 DAG 联动）

- **Dependency**: T4（unit_knowledge_tags/knowledge_tag_id 列在 T1；数据依赖 T2/T4）
- **FR/D**: FR-9；D7
- **实际（2026-11-06）**：端点 `POST /api/admin/taxonomy/courses/generate`（`taxonomy.go:1503`），
  body `{user_id, title, subject?, diagnostic_session_id?, topic_ids?}`（`subject` 当前未参与逻辑）。
  实现为**按 chapter 分组成 unit、unit 间串行 prerequisite_unit_ids**，**非 Kahn 拓扑分层**；
  每 KC 建一条 `practice` 资源（`content_ref=<topic external id>`）与一条 `course_outcomes`
  （mastery_points 80、latest）。**practice 资源的答题端点未见按 `content_ref` 组卷的接线**
  （practice 答题仍走 `/api/taxonomy/practice` 直接传 topic_id/question_id）。
- **Files**: `internal/taxonomy/coursegen.go`（诱导子图 + Kahn 分层 + draft 课程组装）、
  `internal/api/courses.go`（POST /admin/courses/generate-from-kcs、from_diagnosis 入口）、
  `internal/store/courses.go`（unit_knowledge_tags、course_outcomes.knowledge_tag_id DAO）、
  家长端 CoursesTab 生成入口 UI
- **Estimate**: 12h
- **Acceptance**:
  - 生成产物 = 普通 draft 课程：既有 CRUD/发布/克隆/View-As-Learner/解锁引擎零特殊分支
    （用既有 courses_test.go 同款断言跑生成课程）。
  - prerequisite_unit_ids 拓扑正确（构造含 hard/soft 边的 KC 集，层序断言）；
    unit_knowledge_tags 与 course_outcomes.knowledge_tag_id 落库。
  - 「从诊断路径生成」一键：session path_json → draft 课程（集成测试）。
  - 幂等：重复生成产新 draft，不动已发布课程。

### T11: 练习执行 + verify 判分 + 错题回流

- **Dependency**: T10（practice 资源与组卷）、T2（题库）
- **FR/D**: FR-10；D12
- **实际（2026-11-06）**：`POST /api/taxonomy/practice`（`taxonomy.go:312`）服务端判分，
  choice/numeric/short 确定性、self/无法识别→manual（记为 `skipped=true` 事件，不改掌握度）；
  verify 求值器在 `internal/taxonomy/verify.go`/`verify_math.go`。前端 `PracticeRunner` 提供
  不会(skip)/看参考答案自评(对/不对)。**答错不写 `error_items`**（错题本回流未接）；
  `resource_attempts`/`outcome_assessments` 回写未接。
- **Files**: `internal/taxonomy/verify.go`（白名单受限求值器 + fuzz，对拍上游 verify 语义）、
  `internal/api/practice.go`（GET questions 动态组卷 / POST attempt）、
  `internal/api/chores.go` 或 error_items 写侧（practice/retest 来源接 recordErrorItem 同款路径）、
  `web/src/pages/kid/PracticeScreen.tsx`（LaTeX/SVG 渲染 + 降级）、Vitest
- **Estimate**: 14h
- **Acceptance**:
  - verify 求值器：白名单运算单测 + fuzz（注入表达式全部拒绝）；SVG 消毒后渲染无脚本执行（e2e）。
  - 作答回写五件套：resource_attempts / outcome_assessments / learner_events / kc_mastery /
    错题（答错→error_items 带 tag+来源=practice）→ T+0 FSRS 卡自动出现（全链集成测试）。
  - short/self 无 AI 时纯自评可用；AI 仅 advisory（永不拒答）沿用仓库铁律。
  - Outcome 汇总按既有 calculation_method 工作（latest 默认断言）。
  - figure 387 题 / choices 9701 题渲染与判分抽样用例通过（实测覆盖为基线）。
    **数字待对账**：上述 figure/choices 具体数字为初稿口径，本轮未核对，待真实对账后再改
    （代码侧 `internal/taxonomy/data/questions.json` 题总数经核实为 12579，与初稿 12012 口径
    差异同样待对账）。

### T12: 导出/恢复/打印/分享（分享为真实新增实现）

- **Dependency**: T7、T9、T10、T11（有完整数据面可导）
- **FR/D**: FR-12/FR-13；D9/D13
- **实际（2026-11-06；2026-11-07 回写）**：导出 `GET /api/taxonomy/export?user_id=`（schema_version 1；
  `review_items` **恒为空数组**）；**恢复 = `POST /api/admin/taxonomy/import`**（→
  `ImportLearningDocument`，`router.go:436`）；恢复目标由文档里的 `target_user_id` 显式指定
  （前端 `adminImportLearning` 附加；后端必填且必须已存在，否则 400，
  `taxonomy_learning.go:168-182`），会话以新 id 落库；
  **只重建诊断会话**，掌握度/复习卡/练习事件不回写；**无 dry_run**。**数据包重同步是
  `POST /api/admin/taxonomy/sync`**（→ `AdminImportPack`，`router.go:434`），`/import`
  **不再是**数据包重导入（旧描述作废）。`import-learning` **未注册、不是端点**
  （`router.go:434-441` 未见该路径，别名说法作废；`web/src/taxonomy/client.ts:260`、
  `types.ts:459` 仅注释提及，不是调用）。**恢复端点只有 `POST /api/admin/taxonomy/import`**。
  分享：`POST /api/taxonomy/diagnostics/{id}/share`、
  `DELETE /api/admin/taxonomy/shares/{id}`、公开 `GET /api/taxonomy/shared/{token}`（未知/已撤销/
  已过期**一律 404**；`taxonomy.go:1449-1459`）、前端 `/share/taxonomy/{token}`；
  **分享列表端点已实现** `GET /api/admin/taxonomy/shares?user_id=`（`router.go:440`），前端
  `TaxonomySharesCard` 已接入 `TaxonomyTab.tsx:174`，列表/撤销调用见 `web/src/taxonomy/client.ts`；
  e2e **未运行**。打印：分享页有「打印/另存为 PDF」
  按钮与 `@media print` 样式（`workspace.module.css`），**未见**诊断报告/盲区/路径三视图的
  专门打印设计，**未核实**打印效果。
- **Estimate**: 12h
- **Acceptance**:
  - 导出→新库恢复往返测试：孩子诊断/掌握度/错题绑定/FSRS 卡与日志/课程进度恢复可用；
    导出文件不含凭证/PIN/会话（安全断言）。
  - 恢复默认不覆盖服务端更新数据 + dry_run diff 预览；全程审计（操作者/时间）。
  - 分享全链 e2e：创建→无会话打开快照页→打印视图→撤销后 404→过期后 404；
    token ≥128bit、不落日志（中间件断言）；快照语义（事后 override 不改已分享快照）专测。
  - 打印三视图 Chromium 分页合理（Playwright 截图验收）。

### T13: 文档 + 诚实性评审

- **Dependency**: T5、T9、T11、T12、T15（功能面齐）
- **FR/D**: FR-14；design §11；NFR-4
- **实际（2026-11-06）**：`docs/taxonomy.md` 本次新建（家庭使用流程 + 契约命名 + 已知限制 +
  离线边界 + `make dev` 清库提示）。README/ROADMAP 改动：**未做**（非本卡文件范围）。
  全 UI 文案 grep 评审：**pending**（本次只核对代码行为，未逐条评审 UI 文案）。
- **Estimate**: 4h
- **Acceptance**:
  - 文档明示：数据内置随版本分发（用户决策记录）；上游内容 AI 生成未教师审核（质量标识 +
    review_status 治理）；PWA 离线边界如实（缓存可浏览、提交需在线）；分享为新建能力。
  - 内置数据升级/回退运维步骤可照做复现。
  - 全 UI 文案 grep 评审：无未实现能力表述（评审记录附 PR）。

### T14: 回归门禁 + NFR 实测

- **Dependency**: T1–T13、T15
- **FR/D**: NFR-1/2/3/5；FR-14；requirements §4 总验收
- **实际（2026-11-06）**：**全部 pending**。e2e 规格文件已存在（`e2e/tests/taxonomy-kid-learning.spec.ts`、
  `taxonomy-parent.spec.ts`、`taxonomy-share.spec.ts`，helper `e2e/tests/helpers/taxonomy.ts`），
  Go 测试 `internal/api/taxonomy_test.go` 存在；**均未运行**；NFR 指标无实测数据。
- **Files**: 基准脚本、e2e 新用例（`e2e/tests/`：诊断流程、练习流程、图谱浏览、分享页、
  **offline 模拟**）
- **Estimate**: 8h
- **Acceptance**:
  - `make test-all`（Go 集成 + Playwright）+ Vitest 全绿；既有 e2e 两 project 依赖顺序未破坏。
  - NFR-1 四项指标实测数据记录（导入计时/子图 p95/诊断单步 p95/搜索 p95）写入 PR 描述。
  - NFR-2 锁并发测试、NFR-3 权限矩阵测试报告；FR-2 完整性对账断言在 CI 常跑。
  - Playwright offline 用例：断网后壳+图谱可浏览、提交出离线提示（FR-14）。
  - 042 一组迁移 down 全链回滚在真实数据副本上演练一次（记录留存）。
  - requirements §4 总验收清单逐项勾选（**当前全部未勾 = 待实现/待验证**）。

### T15: PWA 离线（service worker 真实新增实现）

- **Dependency**: T5（图谱前端与壳资源清单稳定；可与 T6–T12 并行推进，收口在 T13/T14 前）
- **FR/D**: FR-14；D14
- **实际（2026-11-06；T15 VERSION 于 2026-11-07 回写）**：`web/public/openchore-sw.js`
  （`VERSION = 'v4'`，`openchore-sw.js:122`；导航网络优先回退壳；
  静态资源 SWR；taxonomy **精确白名单**仅 `/api/taxonomy/subjects`、`/api/taxonomy/topics`、
  `/api/taxonomy/topics/{单段 ref}`，其余 network-only；写请求直连；no-store/private 不缓存）；
  注册 `web/src/pwa.ts`（仅 `import.meta.env.PROD`）；认证边界清缓存（AuthContext）。
  Vitest 存在 `web/src/pwa.test.ts`（**未运行**）；离线 Playwright e2e：**pending**。
- **Estimate**: 8h
- **Acceptance**:
  - SW 注册成功且 scope 正确；预缓存壳资源清单与构建产物同步（构建脚本生成或校验，防漂移）。
  - Playwright offline e2e：断网后已访问页面壳与图谱缓存可打开；诊断/练习提交按钮禁用 +
    「离线浏览模式」明示提示；恢复网络后自动回到在线态。
  - VERSION 更新路径测试：新版本 activate 后旧 cache 被清理，无脏壳。
  - 诚实边界：不缓存任何认证响应/他人数据快照；分享页仅缓存壳不缓存 token 数据（隐私）。
  - 摘除注册即回退纯在线 PWA（design §10 降级路径验证）。

## Parallel Execution

> 以下为**初稿并行规划**（历史参考）；实际按 §状态总览 已落地的任务落点见各任务「实际」段。

可并行任务组（不同 Agent 不同 worktree；同文件冲突任务不并行）：

- **Group A（串行起点）**: T1
- **Group B**: T2、T3、T6（T2=taxonomy 包+data 内置+main.go，T3=chores.go+前端词表，
  T6=纯函数包，文件零重叠）
- **Group C**: T4（等 T2/T3）→ T5 与 T8 并行（前端 vs LLM/store，文件不重叠）
- **Group D**: T7（等 T4+T6）→ T9、T10 并行；T15 在 T5 后即可启动
- **Group E**: T11（等 T10）；T12（等 T7/T9/T10/T11 数据面）
- **Group F（收口）**: T13 → T14

冲突红线：`internal/api/router.go`、`internal/model/model.go`、`web/src/api.ts`、
`web/index.html` 是多任务共同触点——按 Group 顺序串行合入，每个 PR 小步 rebase，
不允许两个并行 PR 同时改这些文件。
> 实际观察：taxonomy 的 HTTP handler 集中在 `internal/api/taxonomy.go` 单文件，T4/T7/T9/T10/T12
> 落点高度重叠，并行时须按「单文件单 PR」串行。

## Cumulative Estimate

初稿 T1 6 + T2 14 + T3 6 + T4 12 + T5 14 + T6 14 + T7 12 + T8 10 + T9 10 + T10 12 + T11 14 +
T12 12 + T13 4 + T14 8 + T15 8 = **150h**（规划值，未按实际返工修正）。

## 完成定义（DoD，全卡）

- 每条 FR 至少一条集成/e2e 测试锚定；每条 NFR 有实测数据。
- **迁移仅 042 一组**，up/down 成对且回滚演练过。
- 内置数据完整性对账通过（3108/4625/12579/774/3372）。
- PWA 离线与分享均有 e2e 验收。
- 既有功能零回归（make test-all 全绿）；文档通过诚实性评审。
- **现状（2026-11-06）**：以上 DoD 均无运行证据，全部 pending；未实现项见 requirements §5.3。

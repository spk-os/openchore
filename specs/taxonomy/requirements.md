# Requirements: Taxonomy — cn-math-taxonomy 完整融合规格

> 状态：**规格（Spec）+ 实现核对记录（2026-11-06 回写）**。§1 的 FR/NFR 是验收标准（保持原文
> 措辞作为「目标」）；§0 为 2026-11-05 证据基线；**§5「实现现状核对」**是 2026-11-06 对当前
> taxonomy 分支代码的逐条核对结果（只记录可追溯代码位置的事实，未执行测试的一律标「未运行」）。
> 家庭使用流程见 `docs/taxonomy.md`。
>
> 上游：cn-math-taxonomy（3108 知识点 × 10 学科、4625 hard/soft 先修边、12579 题、774 课标条目、
> diagnose 自适应诊断引擎、图谱浏览/PWA（含 sw.js 离线）、分享 share.js）。
> 上游数据事实以 `internal/taxonomy/data/` 内置副本为准（行数断言见
> `internal/taxonomy/bank_data_test.go` / `catalog_data_test.go`）。
> 用户诉求：**完整融合、易用、真实可用、多学科、课程深度联动** —— 不得退化为「一次性内容导入」
> 或「孤立浏览页面」，每一条上游能力都必须映射到 OpenChore 的既有闭环（任务/错题/复习/课程/画像）。
>
> **用户决策（2026-11-05 冻结）**：① 家庭个人使用，**全量数据直接内置仓库**，license 合规
> 不作为本规格阻塞项；② 本实施契约**只新增 042 一组迁移**（TX-SCHEMA-CODER 正在实现），
> 表名以任务卡为准：`knowledge_tag_prerequisites` / `knowledge_questions` /
> `unit_knowledge_tags` / `course_outcomes.knowledge_tag_id`；③ PWA 离线与分享为**真实新增
> 实现并验收**，不降级为「范围外」。

## 0. 背景与现状盘点（证据基线）

OpenChore 已有的承接资产（taxonomy 分支实测，2026-11-05）：

| 资产 | 位置 | 现状 |
|------|------|------|
| `knowledge_tags`（邻接表：parent_id/code/sort_order/is_system） | `migrations/028_error_items.up.sql` | 仅由试卷分析 AI 判题时经 `EnsureTag`（`internal/store/error_items.go:320`）自由文本落库；无年级/学期/领域/描述/先修边/外部 ID |
| `error_items` + `error_item_tags` + `paper_analyses` | `migrations/028`、`040` | 错题已可绑定 tag，但 tag 是 AI 自由文本产物，无学科知识点体系约束 |
| `review_items`（FSRS，`source_type IN ('error_item','kc','course_outcome')`）+ `review_logs` + `fsrs_params` | `migrations/031_review_items.up.sql` | `'kc'` 与 `'course_outcome'` 两个 source_type 已预留但**尚无写入方**——本规格是它们的第一个真实生产者 |
| `courses` / `course_units`（prerequisite_unit_ids JSON）/ `unit_resources`（resource_type 含 `practice`）/ `resource_attempts` / `course_outcomes` / `outcome_assessments` | `migrations/032_courses.up.sql` | 课程树完整可发布，但**只能手工搭建**；`course_outcomes` 无知识点关联列；`practice` 资源无题库支撑 |
| `learner_events`（xAPI 骨架，append-only）/ `kc_mastery`（Laplace 物化）/ `student_config` | `migrations/033_learner_profile.up.sql` | 按 tag_id 聚合；tag 体系一旦结构化，画像/KC 矩阵立即受益 |
| `learning_measures`（kc_tag_id 可空外键） | `migrations/034_learning_measures.up.sql` | 措施可挂知识点，同样依赖结构化 tag |
| 学科词表（硬编码 5 值 `""/math/chinese/english/physics/chemistry`） | `internal/api/chores.go:1316 isValidSubject`；前端 `web/src/components/admin/PaperAnalysisCard.tsx:18`、`MediaTab.tsx`、`MeasuresTab.tsx`、`pickers.tsx` | **缺** science/morality/history/geography/biology 五学科 |
| token-in-path 只读分享先例（boost links） | `internal/api/router.go:84`（`/api/boost/{token}`） | 可复用的分享模式；学科报告分享链接尚不存在，本规格**真实新增实现**（FR-13） |
| PWA | `web/public/manifest.json` 存在；`web/index.html`/`main.tsx` **未注册任何 service worker** | 可安装、当前无离线能力；本规格**真实新增 SW 离线实现**（FR-14），上游 `sw.js`/`manifest.webmanifest` 为移植参照 |
| 迁移序号 | 最新为 `041_user_read_aloud` | 本规格**只新增 042 一组迁移**（up/down 成对），全部 DDL 合并在内 |

上游内容资产（`/tmp/cnmt-probe/repo` 本地实测，2026-11-05）：

- **知识点 3108**（`data/topics.json`）：前缀↔学科映射已实测确认——`cnm_` math 870 /
  `cnc_` chinese 493 / `cne_` english 791 / `cnp_` physics 269 / `cns_` science 222 /
  `cnd_` morality 159 / `cnh_` history 134 / `cng_` geography 59 / `cnb_` biology 66 /
  `cnk_` chemistry 45。字段实测：id/name/domain/grade(int)/semester(**"上"/"下" 中文串**)/
  chapter/type/description/mastery_criteria(数组)/diagnostic_question(对象)/standards(数组)/
  external/root_reason。
- **先修边 4625**（`data/dependencies.json`，hard 2478 / soft 2147）：字段 topicId/
  prerequisiteId/strength/reason；跨学科边 = 0，学科内自闭环 DAG。
- **题 12579**（`data/questions.json`）：字段覆盖实测——id/topicId/difficulty/type/prompt/
  answer/explanation 全量；verify 12012、choices 9701、figure 387，少量 hideChoiceText/
  unordered/accept/percent。type：choice 9701 / numeric 2849 / short 23 / self 6；
  difficulty：basic 6177 / standard 5048 / challenge 999 / advanced 355。
- **课标 774 条**（`data/cn-curriculum-standards.json`，按学科分组，另含 source/items 元数据键）：
  条目字段实测 id（`cms-2022:1.NA.01` 三段式）/domain/section/clause/text/page_or_ref；
  知识点↔课标关联**内嵌于 topics.json 的 standards 数组，共 3372 条**。另有 `data/mappings/`
  教材版本映射、`data/id-aliases.json`。
- **diagnose 引擎**（`diagnose/engine.js` + app.js + 95 测试）：连续答对 confirm 题判掌握
  （跳过全部祖先）→ 答错出确认题 → 再错判薄弱并沿 hard 先修回溯 → 根因 = 薄弱且先修无薄弱 →
  学习路径按年级升序；纯函数，可移植。
- **PWA/分享参照**（`sw.js`、`manifest.webmanifest`、`diagnose/share.js`）：上游离线缓存策略为
  「壳网络优先断网回缓存、数据缓存优先后台更新、VERSION 手动递增」，可直接作移植蓝本。
- 事实记录：上游许可 CC BY-NC 4.0、内容为 AI 生成未经教师审核（上游 README 明示）。
  **用户已决策**：家庭个人使用、全量内置、license 不作为本规格阻塞；「AI 生成未审核」保留为
  **质量提示**（UI 标识 + 边 review_status 治理），不再是分发阻塞。

## 1. Functional Requirements

### FR-1: 十学科表驱动词表

**WHEN** 系统校验或展示学科（chores.subject、error_items.subject、卷面分析、媒体库、措施、课程）
**THE SYSTEM SHALL** 使用服务端 `subjects` 表作为唯一词表源，覆盖十学科
`math / chinese / english / physics / chemistry / science / morality / history / geography / biology`
（另含空串 = 「非学科」语义保持不变），并向前端提供 `GET /api/subjects`；
`isValidSubject` 与各前端 `SUBJECTS` 常量改为读表/读接口，不再各自硬编码。

Acceptance:
- 旧数据中既有 5 个学科值全部继续有效，空串继续有效（向后兼容，不做数据改写）。
- 新增 5 学科可被 chores/错题/媒体库/措施/卷面分析正常选用与过滤。
- 前端不存在第二份硬编码学科清单（grep 验收）；`subjects` 表含可选 `sb_code` 列
  （JY/T 0644-2022 六位学科编码，已核实的 5 个先填，其余留空待补，不臆造）。
- 原功能零回归：既有学科相关测试全部通过。

### FR-2: 全量数据内置 + 启动幂等导入

**WHEN** 服务启动（迁移之后、HTTP 服务之前）
**THE SYSTEM SHALL** 从**仓库内置数据**（上游 `data/*.json` 全量随仓库分发，go:embed 嵌入，
与 `migrations`/`internal/config/templates` 同机制）把知识点 3108、先修边 4625、题 12579、
课标 774 + 关联 3372 幂等导入 SQLite：以 `external_ref`（上游稳定 id，如 `cnm_p001`）为幂等键
upsert，导入结果（各表计数）返回；导入异常告警并允许手动重试
（`POST /api/admin/taxonomy/sync`，数据包重同步；`/import` 已改为学习恢复，见 §5.1），
**不阻断服务启动**（健壮性兜底）。
> 2026-11-07 回写：数据包指纹存于 settings（`taxonomy.pack_fingerprint` / `taxonomy.pack_synced_at`，
> `internal/api/taxonomy.go:158-159`；闸门逻辑 `SyncTaxonomyPack`，`taxonomy.go:168-200`）：
> 指纹一致时完全跳过加载/解析/upsert 并从库直接计数（`skipped=true`），`force=1` 强制重跑；
> 仍**无** `content_packs` 表（§5-B2 原表述以此为准）。跳过路径的 ≤500ms 耗时指标：**pending（未实测）**。

Acceptance:
- 内置数据完整性校验：导入后各表行数与上游实测一致（3108/4625/12579/774/3372），有断言测试。
- 同一数据重复启动 N 次，各表行数与内容一致（幂等性测试）；指纹未变的重复启动跳过 ≤500ms。
- 数据版本升级能更新既有行、新增缺失行；**永不删除/改写**用户自建 tag（is_system=0）与其关联数据。
- 导入在有限个批量事务内完成（单写约束，见 NFR-2），全量导入耗时满足 NFR-1。
- 各表计数可通过 `GET /api/taxonomy/subjects`（按学科题量统计）与导入响应体查看。
> `GET /api/admin/taxonomy/status` **已实现并接线**（`internal/api/taxonomy_status.go`，
> `router.go:435`，仅 admin）：返回指纹/匹配、最近同步时间、各表计数与 FR-11 绑定比例。
> 其定向用例已经 coder→tester→reviewer 链路评审通过（无阻断缺陷，1 个挂起 minor：
> `internal/store/taxonomy_status.go:52-53` 注释口径）；全量套件未运行，不据此宣称整体验收。

### FR-3: 知识点图谱浏览（家长端 + 孩子端，真实联动）

**WHEN** 家长或孩子打开知识点图谱页并按学科/年级/学期/领域过滤、点选任一知识点
**THE SYSTEM SHALL** 服务端返回子图（节点 + hard/soft 先修边）渲染分层 DAG，详情页展示
描述、掌握标准、课标关联、题量、先修/被先修列表，并**联动**到既有闭环：
该 KC 的错题（error_item_tags）、掌握度（kc_mastery）、复习卡（review_items source_type='kc'）、
相关措施（learning_measures.kc_tag_id）、「从这里生成课程」（FR-9）、「针对它诊断」（FR-5）。

Acceptance:
- 孩子端与家长端都能浏览（孩子端只读、按孩子自己的掌握度着色；家长端可看任意孩子）。
- 图谱不是孤立页面：详情页至少打通错题/掌握度/诊断/课程生成四个入口。
- 3108 节点规模下单学科子图查询与首屏渲染满足 NFR-1；不引入重型图渲染依赖（见 design D10）。

### FR-4: 先修 DAG 维护

**WHEN** 家长在管理端增删先修边，或导入器写入上游边
**THE SYSTEM SHALL** 存储于 `knowledge_tag_prerequisites`（strength hard/soft、reason、
review_status imported/manual/reviewed/rejected，参考 os-taxonomy-beijing 的边分级），
拒绝成环写入（DAG 校验），且被拒绝的边给出可读原因。

Acceptance:
- 成环边写入返回 4xx + 中文可读错误；合法边立即可见并影响诊断回溯（FR-5）与课程拓扑（FR-9）。
- 上游导入边默认 review_status='imported'；家长手工边 'manual'；家长确认后置 'reviewed'。
- 删除边不级联破坏任何会话/课程数据（引用完整性测试）。

### FR-5: 自适应诊断会话（mastered/shaky/weak/unknown + 根因回溯 + 学习路径）

**WHEN** 家长为某孩子发起诊断（限定学科，可选年级/领域范围）
**THE SYSTEM SHALL** 以确定性引擎（上游 `diagnose/engine.js` 语义的 Go 移植，不依赖 LLM）出题：
连续答对 confirm 题判 mastered 并跳过其全部祖先；答错出确认题；再错判 weak 并沿 hard 先修边
向下回溯；根因 = weak 且其先修中无 weak；会话结束产出四态结论
（mastered/shaky/weak/unknown）+ 根因清单 + 按年级升序的学习路径。
全过程（会话、每题作答、每 KC 结论）服务端持久化，孩子端与家长端都可查看。

Acceptance:
- 引擎为纯函数核心（状态机可单测），移植测试覆盖上游语义：掌握跳祖先、确认题、hard 回溯、
  根因判定、路径年级升序，全部有 Go 单测。
- 一次会话中途退出可续答（服务端保存进度）；作答即时落库（append-only）。
- 诊断结论页对家长展示根因链（weak 链条可视化），对孩子展示适龄文案。
- 无 AI 配置时诊断完全可用（确定性引擎，零 LLM 依赖）。

### FR-6: 诊断可撤销 / 恢复 / 更正（服务端持久化）

**WHEN** 家长对已完成（或进行中）的诊断会话执行撤销（void）、对已撤销会话执行恢复（restore），
或对单个 KC 结论执行更正（override 为四态之一，附备注）
**THE SYSTEM SHALL** 全部服务端持久化：void 后该会话不再计入画像/盲区统计但数据保留可恢复；
override 保留引擎原判 + 家长改判 + 时间 + 操作者，画像与下游（FSRS/课程/盲区）一律以
「override 优先于引擎判定」为读序；孩子端与家长端看到一致结果。

Acceptance:
- void→restore 往返后，会话对画像/盲区的贡献恢复原状（可逆性测试）。
- override 有完整审计字段；删除 override 即回到引擎原判。
- 任何状态下不丢原始作答记录（learner_events append-only 不 UPDATE 不 DELETE 的既有约束保持）。

### FR-7: 诊断与练习写画像和 FSRS

**WHEN** 诊断会话结束、或孩子在课程 practice / 重测中作答
**THE SYSTEM SHALL** 写 `learner_events`（verb=answered/attempted，context 带 kc_ids）、
更新 `kc_mastery`（正确/总数 → Laplace 物化），并为 shaky/weak 结论（override 后）的 KC
创建/续期 `review_items(source_type='kc')` FSRS 卡（每 KC 每孩一张，UNIQUE(source_type,source_id)
既有约束）；掌握转好（mastered/override）时挂起（suspend）对应卡而非删除。

Acceptance:
- 诊断结束后可在复习队列（`/api/review/today`）看到新 kc 卡；评分走既有 go-fsrs 路径，不改调度器所有权。
- kc_mastery 与画像页 KC 矩阵随诊断/练习即时更新；low_confidence 语义保持。
- `review_items.source_type='kc'` 与 `'course_outcome'` 从「预留枚举」变为有真实生产者（联调测试）。

### FR-8: 孩子档案盲区历史与再测

**WHEN** 家长或孩子打开学习者画像/档案页
**THE SYSTEM SHALL** 展示该孩子的盲区清单（当前 weak/shaky 的 KC，override 优先）、每个盲区的
来源（哪次诊断/哪些错题）、历史诊断会话列表（含 void 标记），并提供「再测」：对单个盲区 KC
发起小型确认会话或立即生成 FSRS 重测卡；再测通过则盲区解除（状态迁移留痕）。

Acceptance:
- 盲区解除路径完整：再测通过 → finding 更新 → kc 卡挂起 → 画像移除，全程服务端持久化。
- 历史可回溯：任一盲区能看到它产生与解除的完整事件链。
- 孩子端文案适龄（「再挑战」而非「盲区」），家长端全量信息。

### FR-9: 按知识点生成课程（Outcome / practice / 前置 DAG 深度联动）

**WHEN** 家长选择一组 KC（手动勾选、按年级/领域圈选、或「从诊断学习路径生成」）并触发生成
**THE SYSTEM SHALL** 产出 **draft** 课程：KC 先修 DAG 拓扑分层 → `course_units`（层间写
prerequisite_unit_ids，复用既有解锁引擎）；每 unit 挂 `practice` 资源（content_ref 指向该层
KC 的题组）；每个（或每组）KC 关联一条 `course_outcomes`（新增 `course_outcomes.knowledge_tag_id`
列）+ `unit_knowledge_tags` 关联，mastery_points 默认取掌握标准；发布/编辑/克隆/View-As-Learner
走既有课程 API，不另起炉灶。

Acceptance:
- 生成的课程是普通课程：既有课程 CRUD、发布、解锁、进度、Outcome 汇总全部直接可用（零特殊分支）。
- practice 得分写 `resource_attempts` + `outcome_assessments`（assessment_type 按来源），
  Outcome 掌握度按既有 calculation_method 汇总。
- 「从诊断路径生成」一键可用：路径 KC → draft 课程，家长可改后发布。
- 生成是幂等建议而非破坏性操作：重复生成产生新 draft，不改已发布课程。

### FR-10: 练习执行与错题回流

**WHEN** 孩子在 practice 资源或再测中答题
**THE SYSTEM SHALL** 服务端判分（choice 比对 choices/answer；numeric 走 verify 受限表达式
确定性核验；short/self 走自评 + 可选 AI 辅助，AI 永远只是建议），作答写
learner_events/kc_mastery；**答错的题自动落 `error_items`**（带 tag 绑定、subject、
来源=practice/retest），从而接入既有错题本 → 订正任务 → FSRS T+0 复习卡闭环。

Acceptance:
- 练习错题出现在既有错题本（孩子端 ErrorBookScreen / 家长端 ErrorbookTab），带「来自练习」溯源。
- verify 可判定的题型（实测覆盖 12012/12579）不依赖 AI 配置；未配置 AI 时 short/self
  降级为纯自评，功能不缺失。
- figure（实测 387 题内联 SVG）与 LaTeX 题面在孩子端正确渲染（渲染不了的降级为文本 + 提示，不白屏）。

### FR-11: 错题绑定知识点（AI 判题候选集约束）

**WHEN** 试卷分析 AI 判题产出 knowledge_points，或家长手动给错题改绑知识点
**THE SYSTEM SHALL** 优先把自由文本 KC 匹配到已导入的学科候选知识点集（精确 external_ref/name →
规范化模糊匹配 → 无命中才走旧 `EnsureTag` 兜底建 tag），保证 `error_item_tags.tag_id` 尽可能指向
体系内 KC；家长可在错题详情改绑（搜索十学科 KC 树），改绑即时反映到画像/措施/图谱。

Acceptance:
- judge prompt 由自由文本改为按学科注入候选 KC 集（分批/截断策略见 design D11），匹配率可观测
  （status 接口给出「体系内绑定 vs 兜底 tag」比例）。
- 兜底路径保留：候选无命中时，现行为（自由文本建 tag）不变，零回归。
- 家长改绑有 UI、有权限（RequireAdmin）、有即时下游一致性。

### FR-12: 备份 / 恢复 / 导出 / 打印

**WHEN** 家长触发学科学习数据导出、恢复导入、或打印诊断/画像报告
**THE SYSTEM SHALL** 提供：(a) 按孩子导出 JSON 备份（诊断会话+结论、kc_mastery、错题及 tag 绑定、
FSRS 卡与日志、课程进度、learner_events 摘要），含 schema 版本与内置数据版本；(b) 恢复导入
（幂等 upsert，冲突以「导入不覆盖更新的服务端数据」为默认策略并可预览差异）；
(c) 打印友好视图（诊断报告/盲区清单/学习路径，纯前端 @media print，不依赖任何第三方服务）。

Acceptance:
- 导出→清库→恢复的往返测试通过（新实例上孩子学习状态可用）。
- 导出文件不含任何凭证/会话/PIN 数据（安全检查）。
- 打印视图在 Chromium 下分页合理、无背景吞字（e2e 截图验收）。

### FR-13: 报告分享（真实新增实现，token 只读）

**WHEN** 家长为某份诊断报告/学习路径创建分享链接
**THE SYSTEM SHALL** 复用 boost-links 的 token-in-path 模式**新增实现**只读、可撤销、可设过期的
分享端点（无会话即可查看该报告的快照视图，上游 `diagnose/share.js` 为移植参照）；
实现与验收均为本规格交付物，文档如实标注「新建能力」。

Acceptance:
- 链接仅暴露该报告最小数据集（无 PIN/无其他孩子/无原始照片）。
- 家长可列出、撤销分享（目标：列表端点；当前仅创建/撤销，见 §5.1）；撤销后与过期后访问均返回 404。
- 分享页可打开、可打印（e2e 验收）；不引入公开注册/多租户语义，单家庭边界不破。

### FR-14: PWA 离线（真实新增实现）与既有功能保全

**WHEN** 用户安装 PWA 后断网访问，或本规格任何变更合入
**THE SYSTEM SHALL**：**新增实现 service worker 离线能力**（移植参照上游 `sw.js`：站点壳
网络优先断网回缓存；图谱/题库等静态数据缓存优先 + 后台更新；SW VERSION 随数据结构变更递增），
注册于 `web/index.html`/入口；诚实边界：诊断/练习**作答提交等动态写操作需在线**，离线提供
已缓存内容的浏览（UI 明示离线态），不假装全功能离线。同时：本规格所有变更**不得破坏任何
既有功能**（家务/积分/奖励/审批/复习/课程/画像/措施/媒体库），每个 PR 必须全量跑 `make test`
与相关 e2e。

Acceptance:
- SW 注册成功、缓存命中可验证（Playwright offline 模拟：断网后壳与图谱页可打开，动态提交
  给出明确离线提示）。
- SW 更新策略有版本控制（旧缓存可被新版本清理），无「改了代码用户看不到」的脏缓存问题。
- 文档/UI 如实陈述离线边界（可浏览缓存内容，不可离线提交）。
- 既有测试套件（Go 集成 + Playwright + Vitest）全绿是每阶段合入前置条件。

## 2. Non-Functional Requirements

### NFR-1: 性能

- 全量内置数据导入（3108 KC + 4625 边 + 12579 题 + 774 课标 + 3372 关联）：冷启动一次性
  ≤ 60s（目标 ≤ 30s），指纹未变的重复启动跳过耗时 ≤ 500ms。
- 图谱子图查询（单学科 ≤ 900 节点）：API p95 ≤ 300ms；前端首屏可交互 ≤ 1s（本机局域网）。
- 诊断出题/判状态单步：p95 ≤ 100ms（纯内存状态机 + 少量索引查询）。
- 知识点搜索（管理端改绑/生成课程选点）：p95 ≤ 200ms。

### NFR-2: SQLite 单写约束

- 导入与批量写全部走**有限批量事务**（每批 ≤ 500 行量级），批间让出写锁；禁止一个事务包住全量导入。
- 所有新 store 方法短事务、`context.Context`-aware（仓库既有约定）。
- 诊断/练习高频小写（每题一落库）不得与导入并发互卡：导入仅在启动阶段执行。

### NFR-3: 安全与隐私

- 凭证：分享 token 为唯一新增「准凭证」，随机 ≥ 128bit、只读、可撤销、可过期；不落日志。
- 用户数据：全部孩子学习数据仅存本实例 SQLite；导出文件不含认证材料。
- 权限：管理操作（导入/CRUD/生成课程/分享/恢复）一律 `RequireAdmin`；孩子只能读写自己的会话数据
  （沿用既有 kidIDOrSelf 模式）；`X-User-ID` 不信任。
- 审计：诊断 override、void/restore、分享创建/撤销、恢复导入均写审计痕迹（操作者 + 时间）。
- 题面 verify 表达式走白名单受限求值（非 eval）；导入的 figure SVG 消毒（去 script/事件属性）。

### NFR-4: 内容内置与溯源（用户决策记录）

- 用户决策（2026-11-05 冻结）：**家庭个人使用，上游全量数据直接内置仓库随版本分发**；
  license 合规不作为本规格阻塞项、不设外置目录/CI 内容检查等阻塞性设计。
- 保留的良好实践（非阻塞）：导入行 `source='cn-math-taxonomy@<version>'` 溯源；上游内容
  AI 生成未经教师审核 → UI「未审核」质量标识 + 先修边 review_status 供家长逐步确认。
- 内置数据以 `/tmp/cnmt-probe/repo` 本地参考为对账基线（行数断言见 FR-2）。

### NFR-5: 可维护性

- 十学科词表、诊断参数（confirm 连对数等）表驱动/常量集中，不散落硬编码。
- 迁移仅 042 一组，up/down 成对、可回滚；模型只加在 `internal/model/model.go`（仓库铁律）。
- 引擎纯函数 + 表驱动，Go 单测对齐上游语义；集成测试真库 + httptest，无 mock（仓库既有约定）。

## 3. 范围外（明确不做，防蔓延）

- BKT/DKT 掌握度模型升级（kc_mastery 维持 Laplace，升级路径另立规格）。
- CASE CFPackage / LRMI 交换格式导入导出（第二格式，另立任务）。
- 教材目录层在线抓取（国家平台章节树端点 403；内置 `data/mappings/` 静态承接即可）。
- 多家庭/公开题库社区、教师审核工作流系统。
- 高中非 math/english/physics 学科的内容补全（上游未覆盖，不臆造内容）。
- 离线**写操作**（断网提交作答/评分）——FR-14 已如实划界。

## 4. Acceptance Criteria（总验收）

> 勾选规则：只有「已执行并有证据」（测试运行输出 / CI 记录）的条目才可勾选。2026-11-06 核对时
> 本 agent **未运行任何测试**，因此下列复选框保持未勾，逐条的代码级核对结论见 §5。

- [ ] FR-1…FR-14 全部实现且各自 Acceptance 通过（代码层面大体具备，验收未执行；缺口见 §5）
- [ ] NFR-1 有实测数据（导入计时、子图 p95、诊断单步 p95）
- [ ] NFR-2 批量事务策略有并发/锁测试
- [ ] NFR-3 权限矩阵测试 + 分享 token 安全测试 + verify 求值 fuzz
- [ ] 内置数据完整性断言通过（3108/4625/12579/774/3372 对账；断言测试存在于
      `internal/taxonomy/*_data_test.go`，**是否运行通过：未运行**）
- [ ] PWA 离线 e2e（断网壳+图谱可浏览、提交有离线提示）与分享 e2e 通过
- [ ] `make test`、`make test-e2e`、Vitest 全绿；既有功能零回归
- [ ] 042 一组迁移 up/down 成对且 down 回滚演练通过
- [x] 文档（`docs/taxonomy.md`）与 UI 文案通过「诚实性评审」——2026-11-06 新增
      `docs/taxonomy.md`，仅描述代码可核实的当前行为，未实现项单列「已知限制」

## 5. 实现现状核对（2026-11-06，taxonomy 分支）

核对方法：只读 `internal/api/router.go`、`internal/api/taxonomy.go`、`internal/store/taxonomy.go`、
`web/src/taxonomy/*`、`web/public/openchore-sw.js`、`web/src/pwa.ts`、`e2e/tests/taxonomy-*.spec.ts`。
所有指针为「路径:行号」；未运行测试，测试结论只陈述「存在/不存在」。

### 5.1 与设计初稿不一致、已按实现修正的契约

| 项 | 设计初稿（旧） | 当前实现（以代码为准） |
|---|---|---|
| 路由命名 | `/api/knowledge-tags`、`/api/kc-graph`、`/api/admin/diagnostics`、`/api/share/taxonomy/{token}` | `/api/taxonomy/subjects`、`/api/taxonomy/topics[/{externalRef}]`、`/api/taxonomy/practice`、`/api/taxonomy/diagnostics[...]`、`/api/taxonomy/blindspots`、`/api/taxonomy/export`；公开分享 `GET /api/taxonomy/shared/{token}`（`router.go:88-92`）；admin 组 `/api/admin/taxonomy/{sync,import,status,courses/generate,prerequisites*,shares*}`（`router.go:434-441`；`import-learning` 未注册、不是端点） |
| JSON 字段风格 | `externalRef`、`diagnosticSessionId` 等 camelCase | 请求/响应体为 snake_case：`topic_id`、`question_id`、`user_id`、`diagnostic_session_id`、`topic_ids`（`taxonomy.go:285-289,471-480,1491-1494`）；**引擎摘要 `summary` 内部键仍为 camelCase**（`limitReached`/`isRoot`… 见 `internal/taxonomy/engine.go:674` 一带） |
| 诊断撤销/更正模型 | 会话 void/restore + findings override（`override_status`/`override_note`） | 会话级 `undo`（回退答题步骤、重放）/ `correct`（按 attempt 改判并重放）/ `finish`（`taxonomy.go:877,929,1038`），**无** void/restore、无 findings override 表；下游读序来自会话重放后的 state（`persistReplay`，`taxonomy.go:1003`） |
| 诊断范围 | 家长发起、孩子专用端点 | 同一组 `/api/taxonomy/*` 端点由孩子与家长共用，`resolveTarget`/`listTarget` 控制 `user_id`（`taxonomy.go:434,638`） |
| 课程生成 | `/api/admin/courses/generate-from-kcs` | `POST /api/admin/taxonomy/courses/generate`，body `{title, subject, user_id, diagnostic_session_id?, topic_ids?}`（`taxonomy.go:1503-1557`） |
| 分享查询 | 家长列出分享 | 创建 `POST /api/taxonomy/diagnostics/{id}/share`、列表 `GET /api/admin/taxonomy/shares?user_id=`（`router.go:440`，状态派生 `revoked` > `expired` > `active`）、撤销 `DELETE /api/admin/taxonomy/shares/{id}`；家长端 `TaxonomySharesCard` 已接入（`TaxonomyTab.tsx:174`）。列表 e2e：**pending** |
| 恢复导入 | `POST /api/admin/taxonomy/restore` + dry_run diff | **`POST /api/admin/taxonomy/import` = 学习数据恢复**（`ImportLearningDocument`，`router.go:436`），目标由 `target_user_id` 显式指定（必填且必须已存在，`taxonomy_learning.go:168-182`），`user.id` 原样保留作审计；`/import-learning` **未注册、不是端点**（`router.go:434-441` 未见该路径，别名说法作废；恢复端点只有 `/import`）。**`POST /api/admin/taxonomy/sync` = 数据包重同步**（`AdminImportPack`，`router.go:434`）。无 dry_run。恢复流程 e2e：**pending（未运行）** |
| 学科词表校验 | `isValidSubject` 改读表 | `isValidSubject` 仍是 10 值硬编码 switch（`chores.go:1316-1324`）；`subjects` 表由导入器 seed（`DefaultSubjects`，`taxonomy.go:64-80`）供前端 `/api/taxonomy/subjects` 展示 |
| 图谱浏览 | SVG 分层 DAG | 前端为学科 chips + 主题列表 + 详情 + 练习/诊断/盲区/历史四标签工作区（`web/src/taxonomy/TaxonomyWorkspace.tsx:29,61-66`），无图渲染 |

### 5.2 FR 逐条核对（代码事实，不代表验收通过）

- FR-1 十学科：`taxonomy.GetSubjects` 返回按学科题量；前端 `web/src/subjects.ts` 为共享词表；
  服务端校验仍为硬编码 10 值（见上表）。**部分达成**。
- FR-2 导入：启动导入在 `cmd/server/main.go:114-120`（失败仅告警、不阻断），批次 ≤500 行
  （`store/taxonomy.go:283` 一带 `importBatchSize`）；**无** `content_packs` 表，指纹存于
  settings 并由 `SyncTaxonomyPack` 闸门跳过（`taxonomy.go:168-200`）；`GET /api/admin/taxonomy/status`
  **已实现并接线**（`internal/api/taxonomy_status.go`、`router.go:435`）。**达成导入与幂等 upsert、
  指纹跳过与 status 端点；跳过耗时 ≤500ms 未实测（pending）**。
- FR-3 浏览：孩子 `/learn`（`web/src/pages/kid/LearnScreen.tsx`）与家长 Manage「taxonomy」
  （`web/src/components/admin/TaxonomyTab.tsx`）共用工作区；详情含先修/被先修/题/课标
  （`taxonomy.go:159-165`）。**达成（非 DAG 可视化）**。
- FR-4 先修边维护：当前无家长增删先修边的 HTTP 端点；边仅在导入时写入。**未达成**。
- FR-5 诊断：`POST/GET /api/taxonomy/diagnostics[...]`，mode `target|screen`，支持
  `grade/semester/domain/chapter` 范围、`seed`、`avoid`、`limit`、`confirm`（`taxonomy.go:463-505`）。
  **达成**。
- FR-6 撤销/更正：以 undo/correct+重放实现，作答 append-only（`diagnostic_attempts`）；
  无 void/restore 与 findings override 审计列。**以另一机制达成可回退与更正；原验收措辞不适用**。
- FR-7 画像/FSRS：练习与诊断写 `learner_events`、刷新 `kc_mastery`；薄弱 KC 建
  `review_items(source_type='kc')`（`taxonomy.go:380-391` 练习路径、`onDiagnosticFinished`
  `taxonomy.go:851`）。**达成**。
- FR-8 盲区历史再测：`GET /api/taxonomy/blindspots`（weak <0.6 / shaky <0.8 / mastered +
  章节汇总，`taxonomy.go:1145-1175`）、`POST .../retest`（以 weak+shaky 为新 target、避开已出题，
  `taxonomy.go:1075-1127`）。**达成（再测为新会话，不自动关闭旧盲区行）**。
- FR-9 生成课程：`POST /api/admin/taxonomy/courses/generate`，按章节分组成 unit，从诊断
  `summary.path` 或显式 `topic_ids` 取知识点，产出 draft。**达成**。
- FR-10 练习与错题回流：`POST /api/taxonomy/practice` 服务端判分（choice/numeric/short 确定性，
  self/无法识别为 manual → 记录 `skipped=true` 事件、不判错），薄弱 KC 建 FSRS 卡；前端
  `PracticeRunner` 提供「不会（skip）/看参考答案自评（对/不对）」三路真实 POST。
  **注意：练习路径当前不写 `error_items`（错题本回流为设计目标，未接）**。
- FR-11 错题绑定候选集：`internal/taxonomy/judge.go` 是**答案判分归一化**（`JudgeQuestion`/
  `NormalizeText`，供练习/诊断使用），不是 AI 判题候选集；`internal/llm` 中未见对
  `taxonomy` 候选名单的引用，错题改绑端点未见。**未达成（原 D11 落点与当前代码不符，待重新定位）**。
  **2026-11-07 回写**：FR-11 验收口径中的「匹配率可观测」已由 status 端点落实——`binding` 给出
  「体系内 KC 绑定（`is_system=1` 且非空 `external_ref`）/ 全部错题 tag 绑定」比例，按
  `error_items.subject` 分学科，分母为 0 时 ratio 为 `null`（`internal/store/taxonomy_status.go`
  `TaxonomyBindingStats`）。**候选集注入与改绑 UI 仍 pending**。
- FR-12 导出/恢复：`GET /api/taxonomy/export?user_id=`（schema_version 1、user/mastery/events/
  diagnostics/attempts/review_items；`review_items` 当前恒为空数组，`taxonomy.go:1291-1300`）
  + 学习恢复 `POST /api/admin/taxonomy/import`（`ImportLearningDocument`，`target_user_id` 显式指定
  且必须已存在，`taxonomy_learning.go:168-182`；`/import-learning` 未注册、不是端点；会话以新 id 落库）。打印：分享快照页有「打印/另存为 PDF」与通用 `@media print` 规则，
  三视图专门版式未实现、打印效果未核实。**接口达成；恢复仅重建诊断会话，掌握度/复习卡不回写**；
  恢复流程 e2e **pending（未运行）**。
  原设计提到的 `external_ref` 跨机映射仅是概念：实现以 `target_user_id` 显式指定目标（文档 `user.id`
  原样保留作审计）。
- FR-13 分享：token 只读快照（`/api/taxonomy/shared/{token}`；未知/已撤销/已过期**一律 404**，
  `taxonomy.go:1449-1459`），
  快照仅含 session 元信息 + summary（不含原始作答）；前端 `/share/taxonomy/{token}`
  （`web/src/App.tsx:105`）。**达成**。
- FR-14 PWA 离线：`web/public/openchore-sw.js`（VERSION `v4`，`openchore-sw.js:122`；导航网络优先回退壳；静态资源
  stale-while-revalidate；taxonomy 仅缓存 `/api/taxonomy/subjects` 与**`q` 参数全空**的
  `/api/taxonomy/topics` 两个公开只读目录端点——`topics/{ref}` 详情一律 network-only，
  其余 network-only；写请求直连），仅生产构建
  注册（`web/src/pwa.ts`），登录/登出/切用户时清 taxonomy 缓存。**代码达成（边界见 docs）；离线 e2e pending（未运行）**。

### 5.3 主要未决项

1. `subjects` 表驱动校验（FR-1 剩余部分）未实现；FR-4 先修边维护端点**已实现**
   （`PUT /api/admin/taxonomy/prerequisites`、`PATCH /admin/taxonomy/prerequisites/review`，
   `router.go:438-439`），其验收测试是否运行：**pending**。
2. FR-11 错题候选集注入与改绑 UI 未实现（`judge.go` 实为答案判分归一化，设计 D11 的落点需重写）；
   匹配率观测（status `binding`）已实现。
3. 恢复导入不回写 `kc_mastery`/复习卡/练习事件，跨机迁移后画像不完整。
4. `import` 与 `import-learning` 的命名易混淆：前者=学习恢复（唯一恢复端点）；后者**未注册、不是端点**（`router.go:434-441` 未见该路径，别名说法作废）；数据包重同步在 `/sync`。
5. 全量 Go/Vitest/Playwright 用例**未运行**；CI 状态未核实；042 真库回滚演练、figure 消毒 e2e、
   权限矩阵与分享 token 安全测试均 **pending**。


# 十学科学习工作区（taxonomy）使用说明

> 本文只描述**当前代码可核实的行为**（taxonomy 分支，2026-11-06 代码核对；2026-11-07 回写
> FR2-B status 端点、042 迁移落盘、figure/SVG 消毒、FR-11 口径四组已闭环事实，见文末
> 「回写记录」）。未实现或未接通的
> 部分统一列在文末「已知限制」。本页不是验收记录：全量 Go / Vitest / Playwright 套件在文档
> 核对中**未运行**，CI 状态未核实。
>
> 上游数据：cn-math-taxonomy（3108 知识点、4625 先修边、12579 题、774 条课标、3372 条
> 知识点↔课标关联，十个学科），随仓库内置（go:embed），启动时幂等导入。
> 规格见 `specs/taxonomy/{requirements,design,tasks}.md`。

## 1. 入口与角色

| 角色 | 入口 | 说明 |
|------|------|------|
| 孩子 | 孩子端首页「学习」入口卡 → `/learn` | 只读浏览 + 自己的练习/诊断；`user_id` 固定为自己 |
| 家长 | Manage →「学习」组 → 学科（`#tab=taxonomy`） | 顶部先选孩子（默认第一个孩子），再浏览/诊断/盲区/历史；下方是「恢复学习数据」卡；可更正、生成课程、分享 |

两端共用同一个工作区组件，四个标签：

- **学科知识**（孩子）/「学科图谱」（家长）：学科切换、主题列表、知识点详情（描述、掌握标准、
  先修/被先修、题量、课标关联），并有「练习」「诊断这个知识点」入口；
- **挑战检测**（孩子）/「诊断」（家长）：诊断答题流与结果页；
- **再挑战**（孩子）/「盲区」（家长）：盲区清单与再测；
- **历史**：诊断会话历史 + 导出备份（家长端同一工作区上方还有「恢复学习数据」卡）。

孩子端标签用适龄文案（「挑战检测」「再挑战」），家长端用完整术语（「诊断」「盲区」）。

## 2. 家庭使用流程

### 2.1 首次浏览

1. 家长在 Manage → 学习 → 学科中选一个孩子、选学科，浏览主题列表与知识点详情。
2. 详情里的先修关系是**局部**先修图/列表（当前知识点的直接先修与被先修），不是整张学科 DAG。
3. 首次启动会把内置数据导入数据库；导入失败只记日志、不阻断服务，家长可稍后在
   Manage →「学习」的 TaxonomyTab 恢复卡（`data-test="tx-import-card"`）里重新导入数据包
   （见 §4）。

### 2.2 练习

从知识点详情点「练习」进入题库作答：选择题点选、数值/简答题输入后提交，开放题可写思路后
「看参考答案并自评」。每条作答都会真实提交到服务端：

- **自动判题**：选择/数值/简答由服务端确定性判分（数值题走受限求值器，不是 LLM）；
- **不会（skip）**：直接提交「不会」，服务端记为未判分（不判错、不刷新掌握度），展示参考答案；
- **自评（self）**：看参考答案后由孩子选「对 / 不对」，同样真实落库；
- 作答写入学习档案并刷新该知识点掌握度；掌握度 < 0.6 时会创建一张 FSRS 复习卡。
- 答错的题**不会**自动进错题本（见「已知限制」）。
- 从课程单元里的「练习：〈知识点〉」资源进入的是既有课程练习流程；学科题库练习只在「学科知识
  → 知识点 → 练习」入口。

### 2.3 诊断 → skip / self → finish

1. **发起诊断**（家长）：在「诊断」标签选学科，可选年级/学期/领域/章节范围、题量上限；
   典型的全学科筛查用 `screen` 模式，也可从某个知识点「针对它诊断」（`target` 模式）。
   孩子也可以在「挑战检测」里自己发起。
2. **答题**：诊断出题由确定性引擎决定（不依赖 AI、不依赖模型配置）。遇到不会的题可点
   「不会」（skip）；开放题先写思路再提交，服务端记自评（self），孩子可「看参考答案」。
3. **撤销上一题**：答错手误可「撤销最后一题」，服务端回退一步并重新重放会话状态（原作答
   记录保留，只做作废叠加，不删除）。
4. **结束（finish）**：可随时「结束本次检测」，服务端对当前进度出结论（四态：已掌握 /
   不稳 / 薄弱 / 未测）、根因清单与按年级升序的学习路径；结束会为薄弱/不稳的知识点建
   T+0 复习卡。
5. **中途退出**：会话状态在服务端，孩子下次进入可从「历史」续答。

### 2.4 盲区与再测

- 「盲区」标签按掌握度列出薄弱（< 0.6）/不稳（< 0.8）/已掌握，并按章节汇总；孩子端
  显示为「再挑战」。
- 「再测」以当前薄弱 + 不稳的知识点为目标**发起一个新会话**（换一批题），不是在同一会话里
  继续，也不会自动关闭旧会话。
- 再测通过后掌握度自然上升、离开盲区列表；掌握度变好**不会**自动挂起已有的复习卡。

### 2.5 历史与更正

- 「历史」列出该孩子的全部诊断会话，可点开回看结果或续答。
- **更正**（家长，管理端）：对历史作答逐题给「对 / 错 / 不会」的独立改判，服务端以新判定
  重放会话。更正只影响会话结论（摘要、盲区、路径），**不会**回写掌握度与复习卡。

### 2.6 从诊断生成课程

在家长端诊断结果页点「按学习路径生成课程」：服务端取该会话的学习路径（或家长显式选的
知识点），**按章节拆成单元**、单元之间串行解锁（每个单元要求上一单元完成），每个知识点挂一个
练习资源（内容指向该知识点题库）与一条学习目标（满 80 分、按最近一次得分计），生成的是
**draft 课程**。生成后走既有课程管理正常编辑、发布；重复生成会产生新 draft，
不会改动已发布课程。单元里的练习资源目前只是课程内的挂载点，**尚未**打通到学科练习执行器
（见「已知限制」）。

### 2.7 分享与撤销

- 诊断结果页「分享」：创建只读链接（默认 7 天有效，最长 30 天），页面给出
  `…/share/taxonomy/{token}`。链接无需登录即可打开，展示的是**创建时的结论快照**，
  不含原始作答与任何凭证。
- 分享页可「打印 / 另存为 PDF」。
- 撤销与列表（**API 已实现，UI 未接入**）：
  - `GET /api/admin/taxonomy/shares?user_id=<id>`（仅 admin，缺省/非法 `user_id` → 400；返回
    `{shares: [...]}`，空列表为 `[]`，按 id 倒序，状态派生为 `revoked` > `expired` > `active`，
    不含 token hash、原始 token、快照或作答）；
  - `DELETE /api/admin/taxonomy/shares/{id}` 撤销。
  - 家长端**尚无**分享列表界面（`web/src` 未调用上述两个端点），因此"在 UI 里查看/撤销"仍属
    pending；后端对已撤销/已过期的分享 token 返回 404（`GetSharedReport`，与不存在同一状态码）。
- 分享 token 原件不落库（只存 hash）；服务端 token 只存哈希、不写日志；原始链接只在创建响应中
  出现一次，请当场保存。

### 2.8 备份、恢复与跨机迁移

- **导出**：历史标签「导出学习数据（JSON）」（孩子端为「保存我的学习记录」），下载该孩子的
  学习数据文档：`schema_version`、用户信息、掌握度、学习事件、诊断会话、作答记录
  （`review_items` 当前恒为空数组）。
- **恢复端点契约（冻结，HTTP 已接线）**：pack 重同步目标为 `POST /api/admin/taxonomy/sync`，
  学习恢复为 `POST /api/admin/taxonomy/import`（`web/src/taxonomy/client.ts` 的 `adminSync` /
  `adminImportLearning`）。两侧已按冻结契约接线（`internal/api/router.go:434-436`）：
  - `POST /admin/taxonomy/sync` → `AdminImportPack`（数据包重同步 / 手动重试，幂等；
    `internal/api/taxonomy.go` 的 `AdminImportPack`）；
  - `POST /admin/taxonomy/import` → `ImportLearningDocument`（**恢复学习数据**，
    `internal/api/taxonomy_learning.go`）；
  - `POST /admin/taxonomy/import-learning` **不是端点**：`router.go:434-441` 的 admin 路由只有
    sync / status / import / courses/generate / prerequisites PUT / prerequisites 复核 PATCH /
    shares 列表 GET / shares DELETE，未见该路径注册，旧「兼容别名」说法作废（前端
    `web/src/taxonomy/client.ts:260`、`types.ts:459` 仅注释提到，不是调用）。**恢复端点只有
    `POST /api/admin/taxonomy/import`。**
  - 恢复目标由 `target_user_id` 显式指定（前端 `adminImportLearning` 发送时附加；后端必填、
    必须已存在，`taxonomy_learning.go:168-182`）；导出文档里的 `user.id` 原样保留作审计。
    `/import` 不再有「数据包重导入」语义，旧描述（`/import` 语义错配、`/sync` 未接线）已作废。
  - 前端恢复卡的 UI ↔ HTTP 往返（真实上传文件）已写在 `e2e/tests/taxonomy-parent.spec.ts`，
    **该 e2e 本次未运行**，故仍不宣称恢复流程已验收。
- **状态观测**：`GET /api/admin/taxonomy/status`（仅 admin，`router.go:435`）返回只读对账信息：
  `fingerprint` / `fingerprint_match`（内置数据包指纹 vs 已存 setting）、`last_sync_at`
  （从未同步为 `null`）、`topic_count` / `edge_count` / `question_count` / `standard_count`，
  以及 FR-11 绑定比例 `binding`（`internal/api/taxonomy_status.go` 组装
  `PackFingerprint` + `GetSetting` + `CountTaxonomy` + `TaxonomyBindingStats`，
  不改任何数据；计数聚合在 `internal/store/taxonomy_status.go`）。
- **后端恢复端点**：`POST /api/admin/taxonomy/import`（学习恢复，`ImportLearningDocument`；
  `import-learning` 不是端点，见 §2.8）恢复导出文档：目标用户由文档里的 `target_user_id` 显式决定
  （该用户必须已在本机存在），诊断会话以新 id 落库；响应给出
  `imported` / `skipped` 计数。
- **跨机迁移**：在旧机导出 → 新机已有目标孩子（或先建好）→ 在新机导入并显式选目标孩子。
  后端**支持**在文档里另指目标用户（`model.LearningExport.target_user_id`，必填且必须已存在）；
  仍不做基于 `external_ref` 的自动映射，导出文档只带 `user.id` + 家长附加的 `target_user_id`。
  前端卡里"选择孩子作为恢复目标"的 UI 已把选中孩子作为 `target_user_id` 发送。
- **恢复范围**：只重建诊断会话（及其摘要）；掌握度、复习卡、学习事件**不会**回写，
  因此新机上的画像/盲区不会与旧机完全一致。
- 没有 dry-run / 差异预览，恢复前请先导出当前数据留底。

### 2.9 离线边界（PWA）

Service worker（`openchore-sw.js`，`VERSION = 'v4'`，`web/public/openchore-sw.js`）只在**生产构建**注册，开发服务器下不生效。
缓存策略是**精确白名单**，不是前缀放行（`web/public/openchore-sw.js` 的 `isCacheableTaxonomy`）：

| 可离线（此前在线成功访问过才会缓存） | 仅在线（network-only，离线不可用、不回放旧缓存） |
|--------|--------|
| 应用外壳：白名单 SPA 路由导航 → 缓存的 `index.html`；`/assets/…` 与 `/favicon.svg`、`/manifest.json`、`/vite.svg` | 知识点详情 `GET /api/taxonomy/topics/{ref}`（**所有角色**，理由见下） |
| `GET /api/taxonomy/subjects` | 诊断创建、作答、撤销、更正、结束、再测；练习提交 |
| `GET /api/taxonomy/topics`（目录浏览：subject/grade/domain/cursor/limit 参数；自由搜索 `q` 见下） | 盲区、历史、导出、分享（含公开分享页的数据请求）、管理端全部操作 |

要点：

- **`q` 过滤不在离线承诺内**：SW 对 `/api/taxonomy/topics` 额外检查 query——只有 `q` 参数
  全部为空/空白时才可进缓存；任一非空 `q` 一律 network-only（不读缓存回放、不写缓存、
  不剥离 query 统一 key，见 `isCacheableTaxonomy` + `topicsQueryIsBlank`）。**自由搜索本身不承诺
  离线可用**，离线时不应依赖它能返回结果——需要搜索请在线。离线可见的是此前成功访问过的
  无 `q` 目录条目。
- **topic detail 因角色而异，一律不缓存**：服务端对同一 `/api/taxonomy/topics/{ref}` 按角色
  差异化响应——admin 会拿到 `answer`/`accept`/`explanation`，孩子端没有（`internal/api/taxonomy.go`
  `GetTopic` 的 `includeAnswers`）。放进共享缓存会成为越权面，因此该端点在 SW 层对所有角色
  **network-only**（`isCacheableTaxonomy` 精确白名单只有 subjects 与 topics 两个 pathname）。
- **无会话离线访问 `/learn`**：离线只能浏览**已缓存**的公开学科目录（shell + `subjects` /
  `topics` 列表条目）。不伪造用户身份，不开放任何离线写；需要作答、诊断、盲区或详情时必须在线
  重新登录。
- 缓存写入还要求服务端响应 `Cache-Control` 不含 `no-store`/`private`；所有非 GET 请求一律直连。
- 登录、登出、切换用户会清空 taxonomy 缓存（static 缓存一并清），SW 在删除完成前先递增清理代数，
  期间在途的旧响应不会被写回。缓存 `VERSION` 变更时，activate 阶段只删除本应用 `openchore-`
  前缀的旧代缓存。

## 3. 数据与内容来源（注意）

- 知识点、题目、先修边、课标均来自上游 cn-math-taxonomy，**内容为 AI 生成、未经教师审核**，
  用于家庭自用；判题与掌握的结论以参考答案为准，自动判题只是近似。
- AI 功能（任何 OpenAI 兼容模型）在整个产品中都是 **advisory（建议性）**：模型不会拒绝
  一次提交、不直接改分、不决定掌握度；未配置 AI 时学科工作区的浏览、练习、诊断、盲区、
  课程生成、导出恢复全部可用。
- 掌握度阈值：薄弱 < 0.6、不稳 < 0.8、以上为已掌握。

## 4. 运维提示

- `make dev` 会**删除本地数据库**（`openchore.db`/`-shm`/`-wal`）并从示例配置重建，
  学科学习数据与备份都会丢失。需要保留数据时不要用 `make dev`，改用 `make api` / `make ui`。
- 服务启动时自动导入内置数据包（幂等 upsert）；启动导入失败只告警。后端现有的手动入口：
  - `POST /api/admin/taxonomy/sync` —— **数据包重同步**（`AdminImportPack`，幂等；`?force=1` 强制）；
  - `POST /api/admin/taxonomy/import` —— **恢复学习数据**（`ImportLearningDocument`，需 `target_user_id`）；
  - `GET /api/admin/taxonomy/status` —— 只读状态对账（指纹/时间/计数/FR-11 绑定比例）。
  - `import-learning` **不是端点**（`router.go:434-441` 未见注册），别名说法作废。
- **pack 重同步：`POST /api/admin/taxonomy/sync` 已接线（`router.go:434` → `AdminImportPack`，
  幂等手动重试路径）**。前端 `adminSync` 按冻结契约调用该路径；`?force=1` 可强制重跑
  （`AdminImportPack` 读取 `force` 参数）。`POST /api/admin/taxonomy/import` **不再是**数据包
  重导入，而是学习恢复（见下条）。
- **学习恢复：`POST /api/admin/taxonomy/import` 已接线（`router.go:436` → `ImportLearningDocument`）**。
  `target_user_id` 必填且必须已存在（否则 400）；`import-learning` 不是端点（`router.go:434-441`
  未见注册，别名说法作废）。
  前端恢复卡已发送 `target_user_id`；端到端 e2e 未运行，不据此宣称恢复流程已验收。
- **状态对账：`GET /api/admin/taxonomy/status` 已实现**（只读，见 §2.8）。

## 5. 已知限制（以代码为准）

1. **没有整张先修 DAG 可视化**：只有主题列表 + 详情里的局部先修关系。
2. **先修边管理 UI 仅覆盖新增/改写/驳回**：家长端「先修边」卡（`TaxonomyPrerequisitesCard`，
   挂载于 `TaxonomyTab.tsx:172`）走 `PUT /api/admin/taxonomy/prerequisites`（新增/改写，新增即
   reviewed）与 `PATCH .../prerequisites/review`（只发 `rejected`，行保留审计、退出生效图）；
   被驳回边的「恢复」只能回到表单重新 PUT。前端没有任意的 `review_status` 流转与非驳回恢复入口。
   该卡 Vitest 存在（本次未运行），e2e 未覆盖。
3. **练习错题不进错题本**：练习答错不会写入错题/订正，也不会额外写 FSRS 卡（只有该知识点
   掌握度 < 0.6 时才建 kc 复习卡）。
4. **试卷分析判题不会匹配到体系知识点**：AI 判题仍按自由文本建标签，没有学科候选集约束，
   也没有家长改绑知识点的界面。FR-11 只部分落地：`binding` 匹配率已可观测（见 §2.8），
   候选集注入与改绑 UI 仍 pending。
5. **更正/撤销不回写画像**：历史更正、撤销只改会话结论；掌握度、复习卡、学习事件不回滚/不回写。
6. **恢复只有诊断会话（恢复端点只有 `/import`；`import-learning` 不是端点）**：掌握度/复习卡/事件不恢复；无 dry-run；
   `target_user_id` 显式指定且必须已存在（导出文档的 `user.id` 原样保留作审计）；
   `external_ref` 跨机映射未实现。前端 `/import`、`/sync` 与后端已接线（见 §4），
   但恢复流程的端到端 e2e 未运行，**未验收**。
7. **分享列表 UI 已实现、e2e 未跑**：`GET /api/admin/taxonomy/shares?user_id=` 与
   `DELETE /api/admin/taxonomy/shares/{id}` 已实现，前端 `TaxonomySharesCard` 已接入
   `TaxonomyTab`（列表/撤销调用见 `web/src/taxonomy/client.ts`）；端到端验收 pending；
   已撤销/已过期的分享 token 一律 404。
8. **学科校验仍硬编码**：服务端 `isValidSubject` 是固定 10 值，前端改学科词表需同步改代码。
9. **掌握度转好不自动挂起复习卡**：薄弱/不稳时建好的 kc 复习卡不会随着掌握度变好自动 suspend。
10. **课程练习资源不进入题库练习**：生成的 draft 课程里知识点练习资源只是课程挂载点，
    作答走既有课程流程；学科题库练习（写学科掌握度的那个）只在学科知识 → 练习入口。
11. **测试状态**：
    - 已运行的定向用例（有记录，不等于整体验收）：FR2-B status 端点的定向用例已经
      coder→tester→reviewer 链路评审，reviewer 结论「通过」、无阻断缺陷；遗留 1 个 minor：
      `internal/store/taxonomy_status.go:52-53` 注释称 dangling `tag_id` 仍计入分母
      （挂起 minor，未修）。该结论只覆盖 status 端点，不外推到其它模块。
    - **pending（本次与可核实的记录中均未运行，不得写成通过）**：全量 `go test ./...` /
      `go vet ./...` / `npm test -- --run` / `tsc --noEmit` / `npm run build`；
      真实 taxonomy / PWA / share / print 端到端 e2e；042 迁移**真库** up/down 回滚演练、
      `PRAGMA foreign_key_check` 与 `sqlite_master` 全量对比；全量 corpus 导入与查询计划
      （代码侧 `internal/taxonomy/data/questions.json` 实际 **12579 条**，与初稿 12012 口径差异
      待对账）；
      figure 消毒的 E2E 验证；权限矩阵、分享 token 安全、CI 绿灯。

## 6. 回写记录（2026-11-07，卡 7.13-DOC）

本节只登记**已闭环、行为已冻结**的事实回写；未闭环项一律保持 pending，不在本文下结论。

| 回写项 | 回写后的状态 | 证据（代码指针） | 仍 pending |
|---|---|---|---|
| FR2-B status 端点 | 已实现并接线（§2.8、§4） | `internal/api/taxonomy_status.go`；`internal/store/taxonomy_status.go`；`internal/api/router.go:435` | 全量套件；挂起 minor 未修 |
| 042 迁移 | up/down 已落盘成对（`migrations/042_taxonomy.{up,down}.sql`）；down 逆序 drop | 迁移文件存在性 | **真库 up/down 回滚演练、`PRAGMA foreign_key_check`、`sqlite_master` 全量对比** |
| figure / SVG 消毒 | 前端白名单重建渲染，`parseSvgFigure` / `SafeFigure` 已接入练习、诊断、浏览三处题面；入库 `figure_svg` 原样保存，**消毒在渲染层**（`safeSvg.ts` 头部注释 + 调用点核对） | `web/src/taxonomy/safeSvg.ts`、`SafeFigure.tsx`；调用点 `PracticeRunner.tsx:374,396`、`DiagnosticWorkspace.tsx:256,277`、`SubjectBrowser.tsx:521` | figure 消毒的 e2e 验证；按题/选项计数（以主会话裁决为准，本文不写数字） |
| FR-11 口径 | 绑定比例可观测：status 的 `binding` = 体系内 KC 绑定（`is_system=1` 且非空 `external_ref`）/ 全部错题 tag 绑定，按 `error_items.subject` 分学科；分母为 0 时 ratio 为 `null` | `internal/store/taxonomy_status.go` 的 `TaxonomyBindingStats` | 判题候选集注入与家长改绑 UI（仍未实现，见「已知限制」4） |
| 恢复/重同步路由 | `/sync`=数据包重同步、`/import`=学习恢复（`target_user_id` 必填） | `internal/api/router.go:434-436`；`internal/api/taxonomy_learning.go:168-182` | 恢复流程 e2e |
| SW VERSION | `v4`（§2.9） | `web/public/openchore-sw.js:122` | PWA 离线 e2e |

§7.10–7.12 的本规格权威定义不在当前仓库内，本文不引用其结论，相关条款 pending，待主会话裁决。

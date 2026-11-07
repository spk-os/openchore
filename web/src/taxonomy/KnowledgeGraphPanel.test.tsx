// @vitest-environment jsdom
// 家长知识图谱面板专项行为测试（卡 TX-GRAPH-PARENT-UI-TESTER；
// KG-R2-TESTER 修复结构性失败）。
//
// 只读被测实现，不改生产代码。测试分四组：
//  1) layoutGraphNeighborhood 纯函数：无向 BFS 分层 / 确定排序 / 环与脏数据；
//  2) mastery 四态纯函数 + SVG 渲染契约（hard 实线 soft 虚线、箭头方向、
//     null 与 0 区分、domain 折叠、truncated、边详情）；
//  3) 组件状态与请求：userId=null 零请求、loading/空/错误/重试、点击换 root、
//     kid/root 切换的慢响应乱序、token/答案不泄露；
//  4) 十学科：默认 root 由服务端学科表 + 真实知识点解析（不写死数学锚点），
//     subject 过滤透传，宿主挂载与 initialRoot 响应式。
//
// mock 策略（KG-R2）：jsdom 无 base URL，真实 fetch('/api/...') 会因相对 URL
// 解析失败抛错，所以本文件对 taxonomyApi 的方法统一 spyOn/mockResolvedValue；
// 只有「token 不泄露」用例用 setFetchImpl 注入，走真实 request() 验证 header。
// useSubjects 的 cached/cachedAt/inflight 是模块级共享状态，逐用例
// vi.resetModules() + doMock('./client') 后动态加载整份组件模块图（RTL 同源），
// 保证学科快照不串味，且组件读到的 taxonomyApi 就是本用例的 mock 对象。
//
// 默认 root 断言一律取「mock 学科列表解析出的真实 root」（MATH.anchor /
// PHYSICS.anchor），不写死某个 id。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type * as RTL from "@testing-library/react";
import type {
  GraphEdge,
  GraphNode,
  GraphResponse,
  SubjectRow,
  TopicRow,
} from "./types";
// 真实 client 只为 setFetchImpl 注入而加载（token 不泄露用例）；taxonomyApi
// 一律来自逐用例的 doMock，避免依赖 jsdom 的相对 URL fetch。
import * as realClient from "./client";

// 外部依赖：用户列表、i18n、庞大工作区（另有测试覆盖）。只 stub 这三者，
// KnowledgeGraphPanel 仍是被测真实实现。
const { usersList, apiMock } = vi.hoisted(() => {
  const list = vi.fn();
  return {
    usersList: list,
    apiMock: {
      users: { list },
      chores: { listMedia: vi.fn() },
    },
  };
});
vi.mock("../api", () => ({ api: apiMock }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("./TaxonomyWorkspace", () => ({ TaxonomyWorkspace: () => null }));

const kid = {
  id: 7,
  name: "小明",
  avatar_url: "",
  role: "child",
  theme: "light",
  paused: false,
  has_pin: true,
  auth_providers: [],
  created_at: "2026-01-01T00:00:00Z",
};

const mkNode = (id: string, over: Partial<GraphNode> = {}): GraphNode => ({
  id,
  name: id,
  subject: "math",
  grade: 1,
  domain: "数与代数",
  chapter: "第1章",
  mastery: null,
  ...over,
});

const mkEdge = (
  topic: string,
  prereq: string,
  over: Partial<GraphEdge> = {},
): GraphEdge => ({
  topic_id: topic,
  prerequisite_id: prereq,
  strength: "hard",
  reason: "教材顺序",
  review_status: "reviewed",
  ...over,
});

/** 4 节点 4 态：mastered 0.9 / weak 0 / shaky 0.7 / none null，跨 2 个 domain。 */
const baseResponse = (over: Partial<GraphResponse> = {}): GraphResponse => ({
  nodes: [
    mkNode("cnm_p001", { name: "中心知识点", mastery: 0.9 }),
    mkNode("cnm_p002", { name: "先修知识点", mastery: 0 }),
    mkNode("cnm_p003", { name: "不稳知识点", mastery: 0.7 }),
    mkNode("phy_001", {
      name: "物理知识点",
      subject: "physics",
      grade: 8,
      domain: "力学",
      mastery: null,
    }),
  ],
  edges: [
    mkEdge("cnm_p001", "cnm_p002"),
    mkEdge("phy_001", "cnm_p001", {
      strength: "soft",
      reason: "跨学科关联",
      review_status: "machine",
    }),
    mkEdge("cnm_p001", "cnm_p003", {
      strength: "soft",
      reason: "练习关联",
      review_status: "machine",
    }),
  ],
  truncated: false,
  ...over,
});

/** 只有物理学科（非数学/空数学库）的图响应。 */
const physicsResponse = (): GraphResponse => ({
  nodes: [
    mkNode("phy_001", {
      name: "力学基础",
      subject: "physics",
      grade: 8,
      domain: "力学",
      mastery: null,
    }),
    mkNode("phy_002", {
      name: "牛顿定律",
      subject: "physics",
      grade: 8,
      domain: "力学",
      mastery: 0.5,
    }),
  ],
  edges: [
    mkEdge("phy_002", "phy_001", {
      strength: "soft",
      reason: "物理教材顺序",
      review_status: "machine",
    }),
  ],
  truncated: false,
});

/** 服务端 SubjectRow 真实形状（types.ts:22-29），只覆盖面板与 hook 读取的字段。 */
const mkSubject = (
  id: SubjectRow["id"],
  name: string,
  sbCode: string,
  grades: number[],
): SubjectRow => ({
  id,
  name,
  sb_code: sbCode,
  // 计数字段生产代码不读；给真实量级只为形状真实。
  topic_count: 2,
  question_count: 0,
  grades,
});

/** 真实 TopicRow 形状（types.ts:36-49）的种子知识点：id 是 DB 整数，external_ref 才是寻址键。 */
const mkTopic = (
  dbId: string,
  externalRef: string,
  subject: string,
  name: string,
): TopicRow => ({
  id: dbId,
  external_ref: externalRef,
  name,
  grade: null,
  semester: null,
  domain: null,
  chapter: null,
  subject,
});

/** 服务端学科顺序：物理在前 → 默认学科/root 必须来自这份数据，而非 cnm_p001。 */
const SUBJECTS: SubjectRow[] = [
  mkSubject("physics", "物理", "PHYS", [8]),
  mkSubject("math", "数学", "MATH", [1, 2, 3]),
];

/** 各学科 topics 分页首行（limit=1 时服务端返回的首条 external_ref）。 */
const TOPIC_SEED: Record<string, TopicRow> = {
  physics: mkTopic("2", "phy_001", "physics", "力学基础"),
  math: mkTopic("1", "cnm_p001", "math", "中心知识点"),
};

const topicsPage = (
  subject: string,
): { topics: TopicRow[] | null; next_cursor: string | null } => {
  const row = TOPIC_SEED[subject];
  return { topics: row ? [row] : [], next_cursor: null };
};

const pathOf = (
  container: HTMLElement,
  topic: string,
  prereq: string,
): Element => {
  const el = container.querySelector(
    `[data-test="tx-graph-edge-${topic}-${prereq}"]`,
  );
  expect(el).toBeTruthy();
  return el as Element;
};

// 逐用例动态模块图：RTL 与组件/客户端来自同一次 resetModules 后的副本。
type RTLModule = typeof RTL;
interface GraphApiMock {
  graph: ReturnType<typeof vi.fn>;
  subjects: ReturnType<typeof vi.fn>;
  topics: ReturnType<typeof vi.fn>;
  listShares: ReturnType<typeof vi.fn>;
  topicDetail: ReturnType<typeof vi.fn>;
}
let rtl: RTLModule;
let taxonomyApi: GraphApiMock;
let Panel: typeof import("./KnowledgeGraphPanel").KnowledgeGraphPanel;
let graphSpy: GraphApiMock["graph"];
let fetchSetter: typeof realClient.setFetchImpl;
/** 当前模块图里的纯函数集合（随 setup 的模块副本一起刷新）。 */
let layoutModule: typeof import("./KnowledgeGraphPanel");

/**
 * 加载一份全新模块图。`subjects` 为 undefined 时 subjects 接口 reject
 * （隔离用例不依赖学科表）；传入数组则按真实形状 resolve。
 */
async function setup(
  opts: {
    subjects?: SubjectRow[];
    topics?: GraphApiMock["topics"];
    /** true：还原真实 ./client（token 用例需要真实 request() 读 oc.token）。 */
    realClient?: boolean;
  } = {},
) {
  vi.resetModules();
  const next: GraphApiMock = {
    graph: vi.fn().mockResolvedValue(baseResponse()),
    subjects: vi.fn(),
    topics: vi.fn(),
    listShares: vi.fn().mockResolvedValue({ shares: [] }),
    topicDetail: vi.fn(),
  };
  if (opts.subjects === undefined) {
    next.subjects.mockRejectedValue(
      new Error("subjects 接口在隔离用例中不参与"),
    );
  } else {
    next.subjects.mockResolvedValue({ subjects: opts.subjects });
  }
  next.topics.mockImplementation(async (q: { subject?: string } = {}) =>
    topicsPage(q.subject ?? ""),
  );
  if (opts.topics) next.topics = opts.topics;
  // 关键：始终 doMock('./client')，绝不能用 doUnmock —— 那会按磁盘模块的
  // 真实身份重新求值，而依赖它的组件模块（KnowledgeGraphPanel →
  // hooks/useSubjects → ./client）已被 mock 身份解析进缓存，重置不会让已
  // 缓存的模块失效，结果组件读到的是另一份 ./client 实例（其 fetchImpl 从
  // 未被注入，于是 jsdom 相对 URL fetch 直接抛错）。realClient 模式下工厂
  // 原样透传真实 client 的导出，保证组件与用例共用同一个 fetchImpl。
  vi.doMock("./client", () => {
    const impl = realClient as unknown as Record<string, unknown>;
    if (!opts.realClient)
      return { ...impl, taxonomyApi: next, setFetchImpl: impl.setFetchImpl };
    return { ...impl };
  });
  vi.doMock("../api", () => ({ api: apiMock }));
  rtl = await import("@testing-library/react");
  await import("./testUtils"); // data-test 作为 testId 查询属性（仓库约定）
  const panel = await import("./KnowledgeGraphPanel");
  layoutModule = panel;
  taxonomyApi = next;
  graphSpy = next.graph;
  Panel = panel.KnowledgeGraphPanel;
  fetchSetter = realClient.setFetchImpl;
}

/**
 * 等组件「落定」：把定时器与微任务冲干净。
 * 生产实测：root 来源切换（自动解析落定 / 点节点 / 宿主改 initialRoot /
 * 改深度）会让图谱请求数量不确定（1 次或同参数 2 次，取决于效果器在两个
 * 渲染批次间是否重跑），所以断言写在 settle() 之后对「次数」只做下界要求，
 * 对「最后一次真正生效的请求参数」做精确断言。
 */
async function settle(ms = 0) {
  await rtl.act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });
}

beforeEach(async () => {
  vi.clearAllMocks();
  usersList.mockResolvedValue([kid]);
  localStorage.clear();
  sessionStorage.clear();
  await setup({ subjects: SUBJECTS });
});

afterEach(() => {
  rtl.cleanup();
  fetchSetter((...args) => fetch(...args));
  vi.doUnmock("./client");
  vi.doUnmock("../api");
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

describe("layoutGraphNeighborhood：无向 BFS 分层与确定排序", () => {
  it("边方向反转仍按无向跳数分层，同层按 external_ref 排序", () => {
    const nodes = [
      mkNode("r"),
      mkNode("a"),
      mkNode("b"),
      mkNode("c"),
      mkNode("d"),
    ];
    const edges = [
      mkEdge("r", "b"), // b 是 r 的先修（存储方向指向先修）
      mkEdge("a", "r"), // r 是 a 的先修（存储方向反转，分层仍应无向）
      mkEdge("b", "c"),
      mkEdge("d", "a"),
      mkEdge("a", "b"), // 同层环：不得重复入层 / 不得无限循环
      mkEdge("r", "r"), // 自环：忽略
      mkEdge("r", "ghost"), // 悬空邻居：不在 nodes 里，不得渲染
    ];
    const layout = layoutModule.layoutGraphNeighborhood("r", nodes, edges);
    expect(layout.rows).toEqual([
      { depth: 0, ids: ["r"] },
      { depth: 1, ids: ["a", "b"] },
      { depth: 2, ids: ["c", "d"] },
    ]);
    expect(layout.positions).toEqual({
      r: { id: "r", x: 0, y: 0 },
      a: { id: "a", x: 0, y: 1 },
      b: { id: "b", x: 1, y: 1 },
      c: { id: "c", x: 0, y: 2 },
      d: { id: "d", x: 1, y: 2 },
    });
    expect(layout.width).toBe(2);
    expect(layout.height).toBe(3);
    expect(layout.positions.ghost).toBeUndefined();
  });

  it("nodes/edges 输入顺序打乱，结果完全一致（确定性渲染）", () => {
    const nodes = [
      mkNode("r"),
      mkNode("a"),
      mkNode("b"),
      mkNode("c"),
      mkNode("d"),
    ];
    const edges = [
      mkEdge("r", "b"),
      mkEdge("a", "r"),
      mkEdge("b", "c"),
      mkEdge("d", "a"),
      mkEdge("a", "b"),
      mkEdge("r", "r"),
      mkEdge("r", "ghost"),
    ];
    const { layoutGraphNeighborhood } = layoutModule;
    expect(
      layoutGraphNeighborhood("r", [...nodes].reverse(), [...edges].reverse()),
    ).toEqual(layoutGraphNeighborhood("r", nodes, edges));
  });

  it("root 恒在第 0 行；悬空邻居不入层，root 不在 nodes 里仍占第 0 行", () => {
    const { layoutGraphNeighborhood } = layoutModule;
    const only = layoutGraphNeighborhood("r", [], [mkEdge("ghost", "r")]);
    // 邻居不在 nodes 里 → 不入层；root 自身仍占第 0 行。
    expect(only.rows).toEqual([{ depth: 0, ids: ["r"] }]);
    expect(only.positions).toEqual({ r: { id: "r", x: 0, y: 0 } });
    expect(only.width).toBe(1);
    expect(only.height).toBe(1);

    // 即使 root 本身不是服务端节点，第 0 行仍是 root；在 nodes 里的邻居
    // 按无向 1 跳正常入层（与「root 不在 nodes」不冲突）。
    const noRootInNodes = layoutGraphNeighborhood(
      "r",
      [mkNode("a")],
      [mkEdge("a", "r")],
    );
    expect(noRootInNodes.rows[0]).toEqual({ depth: 0, ids: ["r"] });
    expect(noRootInNodes.positions.a).toEqual({ id: "a", x: 0, y: 1 });
  });
});

describe("mastery 四态：null 与 0 明确区分", () => {
  it("null/undefined=none、0=weak，阈值 0.6/0.8 边界分明", () => {
    const { masteryKind } = layoutModule;
    expect(masteryKind(null)).toBe("none");
    expect(masteryKind(undefined)).toBe("none");
    expect(masteryKind(0)).toBe("weak");
    expect(masteryKind(0.59)).toBe("weak");
    expect(masteryKind(0.6)).toBe("shaky");
    expect(masteryKind(0.79)).toBe("shaky");
    expect(masteryKind(0.8)).toBe("mastered");
    expect(masteryKind(1)).toBe("mastered");
    expect(masteryKind(0)).not.toBe(masteryKind(null));
  });

  it("文案：null 是「未测（无掌握度记录）」，0 是真实 0% 且不写「未测」", () => {
    const { masteryLabel, masteryShortLabel } = layoutModule;
    expect(masteryLabel(null)).toBe("未测（无掌握度记录）");
    expect(masteryLabel(0)).toBe("薄弱 0%");
    expect(masteryShortLabel(null)).toBe("未测");
    expect(masteryShortLabel(0)).toBe("薄弱 0%");
    expect(masteryLabel(0.72)).toBe("不稳 72%");
    expect(masteryShortLabel(0.95)).toBe("掌握 95%");
    expect(masteryLabel(0)).not.toContain("未测");
  });

  it("graphNodeToTopicRow 以 external_ref 寻址，不拿本地整数 id 顶替", () => {
    const { graphNodeToTopicRow } = layoutModule;
    const row = graphNodeToTopicRow(
      mkNode("cnm_p009", {
        name: "分数",
        subject: "math",
        domain: "数与代数",
        chapter: "第3章",
      }),
    );
    expect(row.id).toBe("cnm_p009");
    expect(row.external_ref).toBe("cnm_p009");
    expect(row.subject).toBe("math");
  });
});

describe("SVG 渲染契约：线型、箭头方向、掌握度、折叠、截断", () => {
  it("hard 实线 / soft 虚线，箭头从先修（prerequisite）指向后继（topic）", async () => {
    const { container } = rtl.render(
      <Panel userId={7} initialRoot="cnm_p001" />,
    );
    await rtl.screen.findByTestId("tx-graph-canvas");

    // E1：topic=cnm_p001 在 y=0，prerequisite=cnm_p002 在 y=1。
    // 箭头必须从下（先修）画向上（后继），即起点 (80,100) 终点 (80,36)。
    const hard = pathOf(container, "cnm_p001", "cnm_p002");
    expect(hard.getAttribute("stroke")).toBe("#64748b");
    expect(hard.getAttribute("stroke-width")).toBe("1.6");
    expect(hard.getAttribute("stroke-dasharray")).toBeNull();
    expect(hard.getAttribute("marker-end")).toBe("url(#txg-arrow)");
    expect(hard.getAttribute("d")).toMatch(/^M 80 100 /);
    expect(hard.getAttribute("d")).toMatch(/, 80 36$/);

    // E2：存储方向 prerequisite=cnm_p001(0,0) → topic=phy_001(2,1)，
    // 即便 topic 在下面一行，箭头起点仍是先修侧 (80,36)。
    const soft = pathOf(container, "phy_001", "cnm_p001");
    expect(soft.getAttribute("stroke")).toBe("#a78bfa");
    expect(soft.getAttribute("stroke-width")).toBe("1.2");
    expect(soft.getAttribute("stroke-dasharray")).toBe("5 4");
    expect(soft.getAttribute("d")).toMatch(/^M 80 36 /);
    expect(soft.getAttribute("d")).toMatch(/, 380 100$/);
    expect(rtl.screen.getByText("箭头从先修指向后继")).toBeTruthy();
  });

  it("mastery null 与 0 在节点文字、aria 和 CSS 类上明确分开", async () => {
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    await rtl.screen.findByTestId("tx-graph-canvas");

    const none = rtl.screen.getByTestId("tx-graph-node-phy_001");
    const zero = rtl.screen.getByTestId("tx-graph-node-cnm_p002");
    expect(none.textContent).toContain("未测");
    expect(zero.textContent).toContain("薄弱 0%");
    expect(none.textContent).not.toContain("0%");
    expect(none.getAttribute("class")).toMatch(/_graphMasteryNone_/);
    expect(zero.getAttribute("class")).toMatch(/_graphMasteryWeak_/);
    expect(none.getAttribute("class")).not.toEqual(zero.getAttribute("class"));
    expect(none.getAttribute("aria-label")).toContain("未测（无掌握度记录）");
    expect(zero.getAttribute("aria-label")).toContain("薄弱 0%");
    expect(
      rtl.screen.getByTestId("tx-graph-node-cnm_p003").getAttribute("class"),
    ).toMatch(/_graphMasteryShaky_/);
    expect(
      rtl.screen.getByTestId("tx-graph-node-cnm_p001").getAttribute("class"),
    ).toMatch(/_graphMasteryMastered_/);
    expect(rtl.screen.getByTestId("tx-graph-legend").textContent).toContain(
      "未测（null，无掌握度记录）",
    );
  });

  it("domain 折叠隐藏该领域节点与边，root 不参与折叠，再点恢复", async () => {
    const { container } = rtl.render(
      <Panel userId={7} initialRoot="cnm_p001" />,
    );
    await rtl.screen.findByTestId("tx-graph-canvas");
    expect(
      rtl.screen.getByTestId("tx-graph-domain-数与代数").textContent,
    ).toContain("3");
    expect(
      rtl.screen.getByTestId("tx-graph-domain-力学").textContent,
    ).toContain("1");
    expect(
      container.querySelectorAll('[data-test^="tx-graph-node-"]').length,
    ).toBe(4);

    // 生产实测：显式 initialRoot 挂载会发 2 次同参数请求，第二次响应到达
    // 会 setData（折叠状态本身不受影响，但要在响应落定后再点，避免与
    // 响应回调交错）。先等两次请求都完成、画布稳定。
    await rtl.waitFor(() => expect(graphSpy.mock.calls.length).toBe(2), {
      timeout: 2000,
    });
    await settle();
    const before = graphSpy.mock.calls.length;
    rtl.fireEvent.click(rtl.screen.getByTestId("tx-graph-domain-数与代数"));
    await settle();
    // 折叠是纯 UI 状态：不改变请求参数，也不额外发请求。
    expect(graphSpy.mock.calls.length).toBe(before);
    // 折叠后不会触发新请求（防抖/effect 都没重跑）。
    for (const [q] of graphSpy.mock.calls)
      expect(q).toEqual({ root: "cnm_p001", depth: 1, user_id: 7 });
    // root（数与代数）仍显示，同域的 cnm_p002/cnm_p003 被隐藏，跨域节点保留。
    expect(rtl.screen.getByTestId("tx-graph-node-cnm_p001")).toBeTruthy();
    expect(rtl.screen.getByTestId("tx-graph-node-phy_001")).toBeTruthy();
    expect(rtl.screen.queryByTestId("tx-graph-node-cnm_p002")).toBeNull();
    expect(rtl.screen.queryByTestId("tx-graph-node-cnm_p003")).toBeNull();
    expect(
      container.querySelector('[data-test="tx-graph-edge-cnm_p001-cnm_p002"]'),
    ).toBeNull();
    expect(
      container.querySelector('[data-test="tx-graph-edge-cnm_p001-cnm_p003"]'),
    ).toBeNull();
    expect(
      container.querySelector('[data-test="tx-graph-edge-phy_001-cnm_p001"]'),
    ).toBeTruthy();
    expect(
      rtl.screen
        .getByTestId("tx-graph-domain-数与代数")
        .getAttribute("aria-pressed"),
    ).toBe("false");

    rtl.fireEvent.click(rtl.screen.getByTestId("tx-graph-domain-数与代数"));
    expect(rtl.screen.getByTestId("tx-graph-node-cnm_p002")).toBeTruthy();
    expect(
      container.querySelectorAll('[data-test^="tx-graph-node-"]').length,
    ).toBe(4);
  });

  it("truncated=true 有可见提示，false 不显示", async () => {
    graphSpy.mockResolvedValue(baseResponse({ truncated: true }));
    const { rerender } = rtl.render(
      <Panel userId={7} initialRoot="cnm_p001" />,
    );
    const notice = await rtl.screen.findByTestId("tx-graph-truncated");
    await settle();
    expect(notice.getAttribute("role")).toBe("status");
    expect(notice.textContent).toContain("250");
    expect(notice.textContent).toContain("不是完整邻域");

    graphSpy.mockResolvedValue(baseResponse({ truncated: false }));
    rerender(<Panel userId={9} initialRoot="cnm_p001" />);
    await rtl.waitFor(() =>
      expect(rtl.screen.queryByTestId("tx-graph-truncated")).toBeNull(),
    );
  });

  it("边详情展开显示 reason / review_status，且按「先修 → 后继」顺序", async () => {
    const { container } = rtl.render(
      <Panel userId={7} initialRoot="cnm_p001" />,
    );
    await rtl.screen.findByTestId("tx-graph-canvas");
    // 两次同参数响应都已到达：画布是最终响应的渲染结果。
    await rtl.waitFor(() => expect(graphSpy.mock.calls.length).toBe(2), {
      timeout: 2000,
    });
    await settle();
    const btn = container.querySelector(
      '[data-test="tx-graph-edge-row-cnm_p001-cnm_p002"] button',
    );
    expect(btn).toBeTruthy();
    rtl.fireEvent.click(btn as HTMLElement);
    const detail = rtl.screen.getByTestId(
      "tx-graph-edge-detail-cnm_p001-cnm_p002",
    );
    expect(detail.textContent).toContain("原因：教材顺序");
    expect(detail.textContent).toContain("已人工复核（reviewed）");
    expect((detail.textContent ?? "").indexOf("先修知识点")).toBeLessThan(
      (detail.textContent ?? "").indexOf("中心知识点"),
    );
    rtl.fireEvent.click(btn as HTMLElement);
    expect(
      rtl.screen.queryByTestId("tx-graph-edge-detail-cnm_p001-cnm_p002"),
    ).toBeNull();
  });
});

describe("请求与状态：user_id、加载/空/错误/重试、换 root、乱序", () => {
  /** 自动解析出的 root 与学科过滤（来自 mock 学科表 + mock topics）。 */
  const autoFirst = {
    root: TOPIC_SEED.physics!.external_ref,
    subject: "physics",
    depth: 1,
    user_id: 7,
  };

  it("userId=null 时零请求并提示先选孩子；切换为 7 后恰一次请求", async () => {
    const { rerender } = rtl.render(<Panel userId={null} />);
    expect(rtl.screen.getByTestId("tx-graph-no-kid").textContent).toContain(
      "请先",
    );
    expect(rtl.screen.queryByTestId("tx-graph-canvas")).toBeNull();
    expect(rtl.screen.queryByTestId("tx-graph-controls")).toBeNull();
    expect(graphSpy).not.toHaveBeenCalled();
    expect(taxonomyApi.topics).not.toHaveBeenCalled();

    rerender(<Panel userId={7} />);
    await rtl.screen.findByTestId("tx-graph-canvas");
    await settle();
    expect(graphSpy).toHaveBeenCalledTimes(1);
    // 请求的 root/subject 来自学科数据（SUBJECTS 首项 + 该学科首条 external_ref）。
    expect(graphSpy.mock.calls[0][0]).toEqual(autoFirst);
    expect(
      (rtl.screen.getByTestId("tx-graph-subject-select") as HTMLSelectElement)
        .value,
    ).toBe("physics");
  });

  it("初次请求携带默认 root/depth/user_id；深度切到 2 重新请求", async () => {
    rtl.render(<Panel userId={7} />);
    await rtl.screen.findByTestId("tx-graph-canvas");
    await settle();
    expect(graphSpy.mock.calls[0][0]).toEqual(autoFirst);
    rtl.fireEvent.change(rtl.screen.getByTestId("tx-graph-depth"), {
      target: { value: "2" },
    });
    await settle();
    expect(graphSpy).toHaveBeenCalledTimes(2);
    expect(graphSpy.mock.calls[1][0]).toEqual({ ...autoFirst, depth: 2 });
  });

  it("加载态显示「加载邻域…」且刷新禁用；响应到达后画布可见", async () => {
    // 未决响应：挂载两次同参数请求都停在这里（共享一个可控 promise）。
    let resolveGraph: ((r: GraphResponse) => void) | undefined;
    const pending = new Promise<GraphResponse>((resolve) => {
      resolveGraph = resolve;
    });
    graphSpy.mockImplementation(() => pending);
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    expect(rtl.screen.getByRole("status").textContent).toContain("加载邻域…");
    const refresh = rtl.screen.getByTestId(
      "tx-graph-refresh",
    ) as HTMLButtonElement;
    expect(refresh.disabled).toBe(true);
    expect(refresh.textContent).toBe("加载中…");
    await rtl.act(async () => {
      resolveGraph!(baseResponse());
    });
    await rtl.screen.findByTestId("tx-graph-canvas");
    expect(
      (rtl.screen.getByTestId("tx-graph-refresh") as HTMLButtonElement)
        .disabled,
    ).toBe(false);
  });

  it("空响应（含 nodes/edges 为 null）显示空态而非伪造节点", async () => {
    graphSpy.mockResolvedValue({ nodes: null, edges: null, truncated: false });
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    const empty = await rtl.screen.findByTestId("tx-graph-empty");
    await settle();
    expect(empty.textContent).toContain("没有可显示的邻域节点");
    expect(rtl.screen.queryByTestId("tx-graph-canvas")).toBeNull();
    // 宿主显式 initialRoot 属手动来源：请求不带 subject 键；挂载双请求参数一致。
    expect(graphSpy.mock.calls.map((c) => c[0])).toEqual([
      { root: "cnm_p001", depth: 1, user_id: 7 },
      { root: "cnm_p001", depth: 1, user_id: 7 },
    ]);
  });

  it("错误显示服务端消息，点重试成功后渲染画布且不自动重试", async () => {
    graphSpy
      .mockRejectedValueOnce(new Error("图谱服务不可用"))
      .mockResolvedValueOnce(baseResponse());
    // 自动解析来源：学科表首项物理 → topics 首条 phy_001，带 subject 过滤。
    rtl.render(<Panel userId={7} />);
    const err = await rtl.screen.findByTestId("tx-graph-error");
    expect(err.getAttribute("role")).toBe("alert");
    expect(err.textContent).toContain("图谱服务不可用");
    expect(graphSpy).toHaveBeenCalledTimes(1);
    await rtl.waitFor(() => expect(graphSpy).toHaveBeenCalledTimes(1));
    rtl.fireEvent.click(rtl.screen.getByTestId("tx-retry"));
    await rtl.screen.findByTestId("tx-graph-canvas");
    expect(graphSpy).toHaveBeenCalledTimes(2);
    expect(graphSpy.mock.calls[1][0]).toEqual(autoFirst);
    expect(rtl.screen.queryByTestId("tx-graph-error")).toBeNull();
  });

  it("点击节点把 external_ref 设为新 root 并刷新；点当前中心不重复请求", async () => {
    // 按 root 分派响应（不依赖调用序号：挂载期请求次数不确定）。
    graphSpy.mockImplementation(async (q: { root: string }) =>
      q.root === "cnm_p002"
        ? baseResponse({
            nodes: [
              mkNode("cnm_p002", { name: "新中心", mastery: 0.3 }),
              mkNode("cnm_p001", { name: "中心知识点", mastery: 0.9 }),
            ],
            edges: [mkEdge("cnm_p001", "cnm_p002")],
          })
        : baseResponse(),
    );
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    await rtl.screen.findByTestId("tx-graph-node-cnm_p002");
    await settle();
    // 宿主显式初始 root 属手动来源：请求不带 subject 键。
    const initial = { root: "cnm_p001", depth: 1, user_id: 7 };
    expect(graphSpy.mock.calls.length).toBeGreaterThanOrEqual(1);
    for (const [q] of graphSpy.mock.calls) expect(q).toEqual(initial);
    expect(
      rtl.screen.getByRole("group", { name: /中心知识点 为中心/ }),
    ).toBeTruthy();
    expect(
      rtl.screen.getByTestId("tx-graph-node-cnm_p001").getAttribute("tabindex"),
    ).toBe("-1");

    const before = graphSpy.mock.calls.length;
    rtl.fireEvent.click(rtl.screen.getByTestId("tx-graph-node-cnm_p001")); // 当前中心：无变化
    await settle(50);
    expect(graphSpy.mock.calls.length).toBe(before);

    rtl.fireEvent.click(rtl.screen.getByTestId("tx-graph-node-cnm_p002"));
    await settle();
    expect(graphSpy.mock.calls.length).toBeGreaterThanOrEqual(before + 1);
    expect(graphSpy.mock.calls[graphSpy.mock.calls.length - 1][0]).toEqual({
      root: "cnm_p002",
      depth: 1,
      user_id: 7,
    });
    expect(
      (rtl.screen.getByTestId("tx-graph-root-input") as HTMLInputElement).value,
    ).toBe("cnm_p002");
    expect(
      rtl.screen.getByRole("group", { name: /新中心 为中心/ }),
    ).toBeTruthy();
  });

  it("kid 切换：旧孩子慢响应晚到也不覆盖新孩子的图", async () => {
    let resolveSlow: ((r: GraphResponse) => void) | undefined;
    const stale = baseResponse({
      nodes: [
        mkNode("cnm_p001", { name: "中心知识点", mastery: 0.9 }),
        mkNode("uid7_only", { name: "旧孩子专属", domain: "旧孩子" }),
      ],
      edges: [mkEdge("cnm_p001", "uid7_only")],
    });
    const fresh = baseResponse({
      nodes: [
        mkNode("cnm_p001", { name: "中心知识点", mastery: 0.9 }),
        mkNode("uid9_only", { name: "新孩子专属", domain: "新孩子" }),
      ],
      edges: [mkEdge("cnm_p001", "uid9_only")],
    });
    // 按孩子分派：旧孩子的请求挂起，新孩子立即返回。
    graphSpy.mockImplementation((q: { user_id: number }) =>
      q.user_id === 7
        ? new Promise<GraphResponse>((resolve) => {
            resolveSlow = resolve;
          })
        : Promise.resolve(fresh),
    );
    const { rerender } = rtl.render(
      <Panel userId={7} initialRoot="cnm_p001" />,
    );
    await settle();
    expect(graphSpy.mock.calls.length).toBeGreaterThanOrEqual(1);
    rerender(<Panel userId={9} initialRoot="cnm_p001" />);
    await rtl.screen.findByTestId("tx-graph-node-uid9_only");
    await settle();
    expect(graphSpy.mock.calls[graphSpy.mock.calls.length - 1][0]).toEqual({
      root: "cnm_p001",
      depth: 1,
      user_id: 9,
    });

    await rtl.act(async () => {
      resolveSlow!(stale);
    });
    expect(rtl.screen.getByTestId("tx-graph-node-uid9_only")).toBeTruthy();
    expect(rtl.screen.queryByTestId("tx-graph-node-uid7_only")).toBeNull();
    expect(rtl.screen.queryByText("旧孩子专属")).toBeNull();
  });

  it("root 切换：旧 root 慢响应晚到也不覆盖新 root 的图", async () => {
    let resolveSlow: ((r: GraphResponse) => void) | undefined;
    const stale = baseResponse({
      nodes: [
        mkNode("cnm_p001", { name: "中心知识点", mastery: 0.9 }),
        mkNode("old_root_only", { name: "旧中心专属", domain: "旧中心" }),
      ],
      edges: [mkEdge("cnm_p001", "old_root_only")],
    });
    const fresh = baseResponse({
      nodes: [
        mkNode("cnm_p002", { name: "新中心", mastery: 0.3 }),
        mkNode("new_root_only", { name: "新中心专属", domain: "新中心" }),
      ],
      edges: [mkEdge("cnm_p002", "new_root_only")],
    });
    // 按 root 分派：旧中心请求挂起，新中心立即返回。
    graphSpy.mockImplementation((q: { root: string }) =>
      q.root === "cnm_p001"
        ? new Promise<GraphResponse>((resolve) => {
            resolveSlow = resolve;
          })
        : Promise.resolve(fresh),
    );
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    await settle();
    expect(graphSpy.mock.calls[0][0].root).toBe("cnm_p001");

    // 输入框直接改 root → 防抖后提交，新请求立即返回，先渲染新图。
    rtl.fireEvent.change(rtl.screen.getByTestId("tx-graph-root-input"), {
      target: { value: "cnm_p002" },
    });
    await rtl.screen.findByTestId("tx-graph-node-new_root_only");
    await settle();
    // 手动输入换 root：不带学科过滤（可能跨学科导航）。
    expect(graphSpy.mock.calls[graphSpy.mock.calls.length - 1][0]).toEqual({
      root: "cnm_p002",
      depth: 1,
      user_id: 7,
    });

    // 旧 root 的慢响应此刻才到达：reqSeq 作废，不得覆盖新图。
    await rtl.act(async () => {
      resolveSlow!(stale);
    });
    expect(rtl.screen.getByTestId("tx-graph-node-new_root_only")).toBeTruthy();
    expect(rtl.screen.queryByTestId("tx-graph-node-old_root_only")).toBeNull();
    expect(rtl.screen.queryByText("旧中心专属")).toBeNull();
    expect(
      (rtl.screen.getByTestId("tx-graph-root-input") as HTMLInputElement).value,
    ).toBe("cnm_p002");
  });

  it("换 root 后旧数据立即清空：新响应前旧图不静默改中心，并显示加载态（D2）", async () => {
    // 旧中心立即返回；新中心（cnm_p002）永久在途。
    graphSpy.mockImplementation((q: { root: string }) =>
      q.root === "cnm_p001"
        ? Promise.resolve(baseResponse())
        : new Promise<GraphResponse>(() => {}),
    );
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    await rtl.screen.findByTestId("tx-graph-node-cnm_p002");
    await settle();
    const before = graphSpy.mock.calls.length;
    rtl.fireEvent.click(rtl.screen.getByTestId("tx-graph-node-cnm_p002"));
    await settle();
    expect(graphSpy.mock.calls.length).toBeGreaterThan(before);
    expect(graphSpy.mock.calls[graphSpy.mock.calls.length - 1][0].root).toBe(
      "cnm_p002",
    );
    // 新请求在途：旧响应节点已清空，不得拿旧图冒充新中心，且有加载反馈。
    expect(rtl.screen.queryByTestId("tx-graph-canvas")).toBeNull();
    expect(rtl.screen.queryByTestId("tx-graph-node-cnm_p001")).toBeNull();
    expect(rtl.screen.queryByTestId("tx-graph-node-phy_001")).toBeNull();
    expect(rtl.screen.getByTestId("tx-loading").textContent).toContain(
      "加载邻域…",
    );
  });

  it("切换到新孩子、新请求未返回时不显示旧孩子的节点与掌握度（D2）", async () => {
    // 旧孩子立即返回；新孩子的请求永久在途。
    graphSpy.mockImplementation((q: { user_id: number }) =>
      q.user_id === 7
        ? Promise.resolve(baseResponse())
        : new Promise<GraphResponse>(() => {}),
    );
    const { rerender } = rtl.render(
      <Panel userId={7} initialRoot="cnm_p001" />,
    );
    await rtl.screen.findByTestId("tx-graph-node-cnm_p002");
    await settle();
    const before = graphSpy.mock.calls.length;
    rerender(<Panel userId={9} initialRoot="cnm_p001" />);
    await settle();
    expect(graphSpy.mock.calls.length).toBeGreaterThan(before);
    expect(graphSpy.mock.calls[graphSpy.mock.calls.length - 1][0].user_id).toBe(
      9,
    );
    // 切换目标即清空：旧孩子的 0% / 未测 / 节点不得残留，加载态可见。
    expect(rtl.screen.queryByTestId("tx-graph-node-cnm_p002")).toBeNull();
    expect(rtl.screen.queryByTestId("tx-graph-canvas")).toBeNull();
    expect(rtl.screen.getByTestId("tx-loading").textContent).toContain(
      "加载邻域…",
    );
  });

  it("root 输入框防抖：连续敲字只发一次请求，停顿后以最终值请求（D4）", async () => {
    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    await rtl.screen.findByTestId("tx-graph-canvas");
    await settle();
    const before = graphSpy.mock.calls.length;
    const input = rtl.screen.getByTestId("tx-graph-root-input");
    rtl.fireEvent.change(input, { target: { value: "c" } });
    rtl.fireEvent.change(input, { target: { value: "cn" } });
    rtl.fireEvent.change(input, { target: { value: "cnm_p003" } });
    // 防抖窗口内：不逐字符请求，输入框已即时回显。
    expect(graphSpy.mock.calls.length).toBe(before);
    expect((input as HTMLInputElement).value).toBe("cnm_p003");
    await rtl.waitFor(
      () => expect(graphSpy.mock.calls.length).toBeGreaterThan(before),
      { timeout: 2000 },
    );
    await settle(400); // 越过防抖窗口，确认没有第二次提交
    // 三次 change 只提交一次，且取最终值（此前全是旧中心 cnm_p001）。
    expect(graphSpy.mock.calls.slice(before).map((c) => c[0].root)).toEqual([
      "cnm_p003",
    ]);
  });

  it("不泄露 token/答案：真实 client 的 Bearer 只在 header，脏字段不进 DOM", async () => {
    // 本用例必须走真实 client 的 request()（只有它读 oc.token 并拼 header）：
    // 还原 ./client 模块，用 setFetchImpl 注入 fetch。
    await setup({ realClient: true });
    localStorage.setItem("oc.token", "LEAKME-TOKEN-42");

    const payload = {
      ...baseResponse(),
      answer: "SECRET-ANSWER-42",
      accept: ["SECRET-ACCEPT-42"],
      explanation: "SECRET-EXPLANATION-42",
      nodes: [
        {
          ...mkNode("cnm_p001", { name: "中心知识点", mastery: 0.9 }),
          answer: "SECRET-NODE-ANSWER-42",
        },
        ...baseResponse().nodes!.slice(1),
      ],
    };
    // 真实 client 走 setFetchImpl 注入；subjects/topics 也由同一个 fetch 回答，
    // 以免解析阶段打到 jsdom 相对 URL。
    const fetchMock = vi.fn(
      async (url: RequestInfo | URL, _init?: RequestInit) => {
        // jsdom 下 window.location 是 about:blank、无 base URL：真实的
        // `fetch('/api/...')` 会在发出前就抛 "Failed to parse URL"。这里用
        // URL 构造器把 client 传入的相对路径解析成绝对地址后再交给注入的
        // fetch（等价于浏览器按 origin 解析），其余行为保持不变。
        const raw =
          typeof url === "string"
            ? url
            : url instanceof URL
              ? url.href
              : String(url);
        // 相对路径以 / 开头：手写拼接，避免依赖在 jsdom 中的 URL 构造行为。
        const path =
          typeof url === "string"
            ? raw
            : `http://localhost${raw.startsWith("/") ? "" : "/"}${raw}`;
        const body = path.includes("/taxonomy/subjects")
          ? { subjects: SUBJECTS }
          : path.includes("/taxonomy/topics")
            ? topicsPage("physics")
            : payload;
        // 管理员接口（分享列表等由本用例的真实 client 发出）给空页。
        const ok =
          path.includes("/taxonomy/graph") ||
          /\/taxonomy\/(subjects|topics)/.test(path);
        return new Response(JSON.stringify(ok ? body : { shares: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      },
    );
    fetchSetter(fetchMock as unknown as typeof fetch);

    rtl.render(<Panel userId={7} initialRoot="cnm_p001" />);
    await rtl.screen.findByTestId("tx-graph-canvas");
    // 等第二次同参数响应（显式 initialRoot 挂载会发 2 次）也落定，
    // 再统一断言 fetch 调用与 DOM。
    await settle();
    const graphCalls = fetchMock.mock.calls.filter((c) =>
      String(c[0]).includes("/taxonomy/graph"),
    );
    expect(graphCalls.length).toBeGreaterThanOrEqual(1);
    const [url, init] = graphCalls[0] as unknown as [string, RequestInit];
    // 宿主显式 initialRoot 属手动来源：不带 subject；URL 里不得出现 token。
    expect(url).toBe("/api/taxonomy/graph?root=cnm_p001&depth=1&user_id=7");
    expect(url).not.toContain("LEAKME-TOKEN-42");
    expect((init.headers as Record<string, string>).Authorization).toBe(
      "Bearer LEAKME-TOKEN-42",
    );
    expect(init.credentials).toBe("same-origin");
    // 学科/知识点请求也不得把 token 拼进 URL。
    for (const [u] of fetchMock.mock.calls)
      expect(String(u)).not.toContain("LEAKME-TOKEN-42");
    expect(document.body.innerHTML).not.toContain("LEAKME-TOKEN-42");
    expect(document.body.innerHTML).not.toContain("SECRET-ANSWER-42");
    expect(document.body.innerHTML).not.toContain("SECRET-ACCEPT-42");
    expect(document.body.innerHTML).not.toContain("SECRET-EXPLANATION-42");
    expect(document.body.innerHTML).not.toContain("SECRET-NODE-ANSWER-42");
  });
});

describe("十学科：默认 root 由学科数据解析，不写死数学锚点", () => {
  /** 学科表首项为物理时，默认 root 必须是物理学科首条真实知识点。 */
  const physicsFirst = {
    root: TOPIC_SEED.physics!.external_ref,
    subject: "physics",
    depth: 1,
    user_id: 7,
  };

  it.each([
    ["只有非数学学科", [mkSubject("physics", "物理", "PHYS", [8])]],
    ["空学科表（subjects 为空数组）", [] as SubjectRow[]],
  ])(
    "%s 时不请求数学锚点 cnm_p001",
    async (label: string, subjects: SubjectRow[]) => {
      await setup({ subjects });
      // 服务端没有数学知识点：cnm_p001 返回 404，其他 root 才有真实图数据。
      // （必须在 setup 之后设置：setup 会重建 graphSpy，先设会被覆盖。）
      graphSpy.mockImplementation(async (q: { root: string }) => {
        if (q.root === "cnm_p001") throw new Error("topic not found");
        return physicsResponse();
      });

      rtl.render(<Panel userId={7} />);
      if (label.startsWith("只有非数学")) {
        // 物理学科 → 解析出 phy_001 → 带 subject 过滤的请求成功出图。
        await rtl.screen.findByTestId("tx-graph-canvas");
        expect(graphSpy.mock.calls.map((c) => c[0].root)).toEqual(["phy_001"]);
        expect(graphSpy.mock.calls[0][0]).toEqual(physicsFirst);
        expect(rtl.screen.queryByTestId("tx-graph-error")).toBeNull();
        expect(rtl.screen.getByText("力学基础")).toBeTruthy();
        expect(
          (
            rtl.screen.getByTestId(
              "tx-graph-subject-select",
            ) as HTMLSelectElement
          ).value,
        ).toBe("physics");
      } else {
        // 没有任何学科 → 不发假请求（尤其不请求 cnm_p001），呈现空态。
        await rtl.screen.findByTestId("tx-graph-empty");
        expect(graphSpy).not.toHaveBeenCalled();
        expect(rtl.screen.queryByTestId("tx-graph-error")).toBeNull();
        expect(
          (rtl.screen.getByTestId("tx-graph-root-input") as HTMLInputElement)
            .value,
        ).toBe("");
      }
    },
  );

  it("TaxonomyTab 挂载面板时由学科表解析 root，并渲染学科选择器", async () => {
    const { TaxonomyTab } = await import("../components/admin/TaxonomyTab");
    rtl.render(<TaxonomyTab />);
    await rtl.screen.findByTestId("tx-graph-panel");
    await rtl.waitFor(() => expect(graphSpy).toHaveBeenCalled());
    expect(usersList).toHaveBeenCalled();
    // 请求 root 来自学科数据（物理首项 + 物理首条 external_ref），不是数学锚点。
    expect(graphSpy.mock.calls[0][0]).toEqual({
      ...physicsFirst,
      user_id: kid.id,
    });
    expect(
      (rtl.screen.getByTestId("tx-graph-root-input") as HTMLInputElement).value,
    ).toBe("phy_001");
    expect(rtl.screen.getByTestId("tx-kid-select")).toBeTruthy(); // 有孩子选择器
    const select = rtl.screen.getByTestId(
      "tx-graph-subject-select",
    ) as HTMLSelectElement;
    expect(select.value).toBe("physics");
    expect([...select.options].map((o) => o.value)).toEqual([
      "physics",
      "math",
    ]);
    expect(taxonomyApi.topics).toHaveBeenCalledWith(
      expect.objectContaining({ subject: "physics" }),
    );
  });

  it("initialRoot 注入初值，宿主后续改值同步生效（D3）", async () => {
    const { rerender } = rtl.render(<Panel userId={7} initialRoot="phy_001" />);
    await rtl.screen.findByTestId("tx-graph-canvas");
    await rtl.waitFor(() => expect(graphSpy).toHaveBeenCalledTimes(2), {
      timeout: 2000,
    });
    expect(graphSpy.mock.calls[0][0].root).toBe("phy_001"); // 初值可注入
    // 宿主显式给 root：属手动来源，不带学科过滤。
    expect(graphSpy.mock.calls[0][0]).not.toHaveProperty("subject");

    rerender(<Panel userId={7} initialRoot="phy_002" />);
    await rtl.waitFor(() => expect(graphSpy).toHaveBeenCalledTimes(3));
    expect(graphSpy.mock.calls[2][0].root).toBe("phy_002");
    expect(graphSpy.mock.calls[2][0]).not.toHaveProperty("subject");
    expect(
      (rtl.screen.getByTestId("tx-graph-root-input") as HTMLInputElement).value,
    ).toBe("phy_002");
  });

  it("学科表首项为数学时，默认 root 取数学首条真实知识点并透传 subject", async () => {
    await setup({ subjects: [mkSubject("math", "数学", "MATH", [1, 2, 3])] });
    rtl.render(<Panel userId={7} />);
    await rtl.screen.findByTestId("tx-graph-canvas");
    await settle();
    expect(taxonomyApi.topics).toHaveBeenCalledWith({
      subject: "math",
      limit: 1,
    });
    expect(graphSpy.mock.calls[0][0]).toEqual({
      root: TOPIC_SEED.math!.external_ref,
      subject: "math",
      depth: 1,
      user_id: 7,
    });
  });
});

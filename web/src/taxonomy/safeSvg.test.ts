// @vitest-environment jsdom
// SVG 安全净化（卡 TX-FE-CODER）：题库 387 题 figure 的白名单解析。
// 攻击向量逐个断言：script/foreignObject/image/use/动画/事件属性/外链/注释包装。

import { describe, expect, it } from 'vitest';
import { isSafeSvg, parseSvgFigure } from './safeSvg';

const goodCircle = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><circle cx="50" cy="50" r="40" fill="none" stroke="currentColor" stroke-width="2.6"/></svg>';

describe('parseSvgFigure', () => {
  it('接受纯几何图形并保留绘制属性', () => {
    const parsed = parseSvgFigure(goodCircle);
    expect(parsed).not.toBeNull();
    expect(parsed!.safe).toBe(true);
    const root = parsed!.nodes[0];
    expect(root.kind).toBe('element');
    if (root.kind !== 'element') return;
    expect(root.tag).toBe('svg');
    // 属性保留源大小写（SVG attribute 大小写敏感：viewBox）。
    expect(root.attrs['viewBox']).toBe('0 0 100 100');
    expect(root.children).toHaveLength(1);
    const circle = root.children[0];
    expect(circle.kind).toBe('element');
    if (circle.kind !== 'element') return;
    expect(circle.attrs['stroke-width']).toBe('2.6');
  });

  it('保留文本节点内容', () => {
    const parsed = parseSvgFigure('<svg><text x="0" y="12">hello</text></svg>');
    expect(parsed!.safe).toBe(true);
    const root = parsed!.nodes[0];
    if (root.kind !== 'element') throw new Error('expected element');
    const text = root.children[0];
    if (text.kind !== 'element') throw new Error('expected element');
    const child = text.children[0];
    if (child.kind !== 'text') throw new Error('expected text');
    expect(child.text).toBe('hello');
  });

  it('拒绝非 svg 输入、空串、undefined', () => {
    expect(parseSvgFigure('<div>x</div>')).toBeNull();
    expect(parseSvgFigure('')).toBeNull();
    expect(parseSvgFigure(null)).toBeNull();
    expect(parseSvgFigure(undefined)).toBeNull();
  });

  it('script 标签整段拒绝', () => {
    const bad = `<svg><circle cx="1" cy="1" r="1"/><script>alert(1)</script></svg>`;
    const parsed = parseSvgFigure(bad);
    expect(parsed).not.toBeNull();
    expect(parsed!.safe).toBe(false);
  });

  it('大小写混淆的 script 也拒绝', () => {
    const bad = `<svg><ScRiPt>alert(1)</ScRiPt></svg>`;
    const parsed = parseSvgFigure(bad);
    expect(parsed!.safe).toBe(false);
  });

  it('foreignObject 拒绝', () => {
    const bad = `<svg><foreignObject><div onclick="x">hi</div></foreignObject></svg>`;
    expect(parseSvgFigure(bad)!.safe).toBe(false);
  });

  it('image（外链位图）拒绝', () => {
    const bad = `<svg><image href="https://evil/x.png"/></svg>`;
    expect(parseSvgFigure(bad)!.safe).toBe(false);
  });

  it('use（引用注入）拒绝', () => {
    const bad = `<svg><use href="#x"/></svg>`;
    expect(parseSvgFigure(bad)!.safe).toBe(false);
  });

  it('SMIL 动画（attributeName=href 注入向量）拒绝', () => {
    const bad = `<svg><a><animate attributeName="href" values="javascript:alert(1)"/><text>x</text></a></svg>`;
    expect(parseSvgFigure(bad)!.safe).toBe(false);
  });

  it('on* 事件属性被剥离（标签安全时属性丢弃、元素保留）', () => {
    const sneaky = `<svg onload="alert(1)"><circle cx="1" cy="1" r="1" onclick="steal()"/></svg>`;
    const parsed = parseSvgFigure(sneaky);
    expect(parsed).not.toBeNull();
    expect(parsed!.safe).toBe(true);
    const root = parsed!.nodes[0];
    if (root.kind !== 'element') throw new Error('expected element');
    expect(root.attrs['onload']).toBeUndefined();
    const circle = root.children[0];
    if (circle.kind !== 'element') throw new Error('expected element');
    expect(circle.attrs['onclick']).toBeUndefined();
  });

  it('href/xlink:href/style/src 属性被剥离', () => {
    const parsed = parseSvgFigure(
      `<svg><a href="https://x"><text style="background:url(javascript:1)">t</text></a></svg>`,
    );
    // <a> 在标签黑名单里 → 整段拒绝
    expect(parsed!.safe).toBe(false);
  });

  it('url(...) 样式注入被属性值检查拒绝', () => {
    const parsed = parseSvgFigure(
      `<svg><rect fill="url(javascript:alert(1))" width="1" height="1"/></svg>`,
    );
    expect(parsed).not.toBeNull();
    expect(parsed!.safe).toBe(false);
  });

  it('非白名单标签（如 iframe/marker 外的未知标签）丢弃但不致 unsafe', () => {
    const parsed = parseSvgFigure(`<svg><circle cx="1" cy="1" r="1"/><foobar>x</foobar></svg>`);
    expect(parsed!.safe).toBe(true);
    const root = parsed!.nodes[0];
    if (root.kind !== 'element') throw new Error('expected element');
    expect(root.children).toHaveLength(1); // foobar 丢了，circle 留着
  });

  it('畸形 XML（text/html 宽容解析后无有效子元素）安全降级为空 svg', () => {
    // text/html 解析器不报错，产出一个空 <svg>——净化后是无害空图。
    const parsed = parseSvgFigure('<svg><circle');
    expect(parsed).not.toBeNull();
    expect(parsed!.safe).toBe(true);
    expect(parsed!.nodes).toHaveLength(1);
  });

  it('isSafeSvg 谓词与 parse 一致', () => {
    expect(isSafeSvg(goodCircle)).toBe(true);
    expect(isSafeSvg('<svg><script/></svg>')).toBe(false);
    expect(isSafeSvg('plain text')).toBe(false);
    expect(isSafeSvg(null)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// B. 补齐缺失攻击向量（reviewer 清单）
// ---------------------------------------------------------------------------

/** 递归收集净化树里的全部元素节点。 */
function collectElements(nodes: SvgNode[]): Extract<SvgNode, { kind: 'element' }>[] {
  const out: Extract<SvgNode, { kind: 'element' }>[] = [];
  const walk = (list: SvgNode[]) => {
    for (const n of list) {
      if (n.kind === 'element') {
        out.push(n);
        walk(n.children);
      }
    }
  };
  walk(nodes);
  return out;
}

/** 收集 sanitizeElement 里做过拒绝的步骤：safe=false 时 nodes 必为空（整段拒绝）。 */
function expectRejected(input: string) {
  const parsed = parseSvgFigure(input);
  expect(parsed).not.toBeNull();
  expect(parsed!.safe).toBe(false);
  expect(parsed!.nodes).toHaveLength(0);
  expect(isSafeSvg(input)).toBe(false);
}

describe('攻击向量补强（B 组）', () => {
  it('<style> 块（CSS 注入 / @import / expression()）不得进入 nodes', () => {
    const inputs = [
      `<svg><style>@import url("https://evil/x.css");</style><circle r="1"/></svg>`,
      `<svg><style>circle{fill:expression(alert(1))}</style></svg>`,
      `<svg><style>rect{background:url(javascript:alert(1))}</style></svg>`,
    ];
    for (const input of inputs) {
      const parsed = parseSvgFigure(input);
      expect(parsed).not.toBeNull();
      expect(parsed!.safe).toBe(true);
      const els = collectElements(parsed!.nodes);
      expect(els.map((e) => e.tag)).not.toContain('style');
      for (const el of els) {
        for (const [k, v] of Object.entries(el.attrs)) {
          expect(k.toLowerCase()).not.toBe('style');
          expect(v.toLowerCase()).not.toContain('expression(');
          expect(v.toLowerCase()).not.toContain('@import');
        }
      }
    }
  });

  it('xlink:href / XLINK:HREF / 混合大小写命名空间前缀被拦截', () => {
    const inputs = [
      `<svg><a xlink:href="https://evil/x">t</a></svg>`,
      `<svg><a XLINK:HREF="javascript:alert(1)">t</a></svg>`,
      `<svg><text xLink:Href="https://evil/x">t</text></svg>`,
      `<svg><rect HREF="https://evil/x" width="1" height="1"/></svg>`,
    ];
    for (const input of inputs) {
      const parsed = parseSvgFigure(input);
      expect(parsed).not.toBeNull();
      // <a> 分支整段拒绝；其它分支即使放行也必须不存在 href 类属性。
      if (parsed!.safe) {
        for (const el of collectElements(parsed!.nodes)) {
          for (const k of Object.keys(el.attrs)) {
            expect(k.toLowerCase()).not.toMatch(/href/);
          }
        }
      } else {
        expect(parsed!.nodes).toHaveLength(0);
      }
    }
    // 外部符号引用必须整段拒绝
    expectRejected(`<svg><a xlink:href="https://evil/x">t</a></svg>`);
    expectRejected(`<svg><a XLINK:HREF="javascript:alert(1)">t</a></svg>`);
  });

  it('超长属性值被有界处理（不崩溃、不无限耗时）', () => {
    const longVal = 'A'.repeat(200_000);
    const input = `<svg><path d="M0 0 ${longVal}"/></svg>`;
    const t0 = performance.now();
    const parsed = parseSvgFigure(input);
    const elapsed = performance.now() - t0;
    expect(elapsed).toBeLessThan(5_000);
    expect(parsed).not.toBeNull();
    // 纯 d 属性属于白名单，存在与否都可接受，但必须能解析或安全拒绝
    expect(parsed!.safe).toBe(parsed!.safe === true || parsed!.safe === false);
  });

  it('极深嵌套（5000 层 <g>）不栈溢出且耗时有限', () => {
    const depth = 5_000;
    const input = `<svg>${'<g>'.repeat(depth)}<circle r="1"/>${'</g>'.repeat(depth)}</svg>`;
    const t0 = performance.now();
    let parsed: ReturnType<typeof parseSvgFigure>;
    let thrown: unknown = null;
    try {
      parsed = parseSvgFigure(input);
    } catch (e) {
      thrown = e;
    }
    const elapsed = performance.now() - t0;
    expect(thrown).toBeNull();
    expect(elapsed).toBeLessThan(10_000);
    expect(parsed!).not.toBeNull();
    // 允许 null（拒绝）或 safe=false（拒绝），唯独不允许抛错/挂死。
    if (parsed! !== null) {
      expect(typeof parsed!.safe).toBe('boolean');
      if (parsed!.safe) expect(parsed!.nodes.length).toBeGreaterThan(0);
    }
  });

  it('<![CDATA[ 包裹的 <script> 与 CSS 被拦截或剥离', () => {
    expectRejected(`<svg><![CDATA[<script>alert(1)</script>]]></svg>`);
    const parsed = parseSvgFigure(`<svg><style><![CDATA[body{color:red}]]></style></svg>`);
    expect(parsed).not.toBeNull();
    expect(collectElements(parsed!.nodes).map((e) => e.tag)).not.toContain('style');
  });

  it('HTML 注释包裹的危险标签：仅在含危险标签时整段拒绝', () => {
    expectRejected(`<svg><!-- <script>alert(1)</script> --><circle r="1"/></svg>`);
    const benign = parseSvgFigure(`<svg><!-- harmless note --><circle r="1"/></svg>`);
    expect(benign).not.toBeNull();
    expect(benign!.safe).toBe(true);
    for (const el of collectElements(benign!.nodes)) {
      expect(el.tag).not.toBe('script');
      for (const k of Object.keys(el.attrs)) expect(k.toLowerCase()).not.toMatch(/^on/);
    }
  });

  it('大小写混淆标签（<ScRiPt> / <FOREIGNOBJECT>）不得绕过白名单', () => {
    expectRejected(`<svg><ScRiPt>alert(1)</ScRiPt></svg>`);
    expectRejected(`<svg><FOREIGNOBJECT><div>x</div></FOREIGNOBJECT></svg>`);
    expectRejected(`<svg><FoReIgNoBjEcT/></svg>`);
    expectRejected(`<svg><foreignobject><style>@import url(//e)</style></foreignobject></svg>`);
    // 大小写混淆的 on* 事件属性也要剥离
    const evt = parseSvgFigure(`<svg><circle OnClick="alert(1)" onLOAD="x()" r="1"/></svg>`);
    expect(evt).not.toBeNull();
    for (const el of collectElements(evt!.nodes)) {
      for (const k of Object.keys(el.attrs)) expect(k.toLowerCase()).not.toMatch(/^on/);
    }
  });
});

// ---------------------------------------------------------------------------
// A. 遍历内置题库全部真实 figure（数量以数据文件为准，不写死）
// ---------------------------------------------------------------------------

// Vitest 跑在 node 里但 app tsconfig 不带 node types：照 pwa.test.ts 惯例，
// 动态 import + 手写最小类型读取数据文件（路径含 web/ 之外的 internal/）。
type FsModule = { readFileSync: (path: string, enc: string) => string };
type PathModule = { dirname: (p: string) => string; resolve: (...p: string[]) => string };
type UrlModule = { fileURLToPath: (u: string) => string };
const fsMod: FsModule =
  (await import(/* @vite-ignore */ ('node:fs' as string))) as never;
const pathMod: PathModule =
  (await import(/* @vite-ignore */ ('node:path' as string))) as never;
const urlMod: UrlModule =
  (await import(/* @vite-ignore */ ('node:url' as string))) as never;
import type { SvgNode } from './safeSvg';

interface QuestionWithFigure {
  id?: string;
  figure?: string;
  options?: { figure?: string }[];
  [k: string]: unknown;
}

/** 递归收集任意 JSON 结构里的非空 figure 字符串（以及其所在题目 id 的近似定位）。 */
function collectFigures(value: unknown, path: string, out: { where: string; figure: string }[]): void {
  if (Array.isArray(value)) {
    value.forEach((v, i) => collectFigures(v, `${path}[${i}]`, out));
    return;
  }
  if (value && typeof value === 'object') {
    const obj = value as Record<string, unknown>;
    const id = typeof obj.id === 'string' ? obj.id : typeof obj.id === 'number' ? String(obj.id) : '';
    for (const [k, v] of Object.entries(obj)) {
      const next = `${path}.${k}`;
      if (k === 'figure' && typeof v === 'string' && v.trim()) {
        out.push({ where: id || path, figure: v });
      } else {
        collectFigures(v, next, out);
      }
    }
  }
}

const DATA_PATH = pathMod.resolve(
  pathMod.dirname(urlMod.fileURLToPath(import.meta.url)),
  '../../../internal/taxonomy/data/questions.json',
);

// 生产净化器契约的禁止项（与 safeSvg.ts 的 TAG_DENYLIST / 属性规则对齐）
const FORBIDDEN_TAGS = new Set([
  'script', 'foreignobject', 'image', 'iframe', 'use', 'a', 'handler', 'listener',
  'animate', 'animatetransform', 'animatemotion', 'set', 'style',
]);
// 题库只允许的标签集合（若数据里出现白名单外标签，说明数据或净化器有变动，需人工看）
const EXPECTED_TAGS = new Set([
  'svg', 'g', 'defs', 'title', 'desc', 'path', 'rect', 'circle', 'ellipse', 'line',
  'polyline', 'polygon', 'text', 'tspan', 'textpath', 'marker', 'clippath', 'mask',
  'pattern', 'lineargradient', 'radialgradient', 'stop', 'filter',
]);

describe('内置题库全量 figure 消毒（A 组，真实数据）', () => {
  const collected: { where: string; figure: string }[] = [];
  // 题库根是题目对象数组（12579 题，以数据文件为准），元素即 QuestionWithFigure。
  const questions: QuestionWithFigure[] = JSON.parse(fsMod.readFileSync(DATA_PATH, 'utf-8'));
  collectFigures(questions, '', collected);
  const unique = new Map<string, { where: string; figure: string }>();
  for (const c of collected) if (!unique.has(c.figure)) unique.set(c.figure, c);
  const entries = Array.from(unique.values());

  it('数据文件确实包含 figure 引用（动态计数，不写死）', () => {
    expect(collected.length).toBeGreaterThan(0);
    expect(entries.length).toBeGreaterThan(0);
    // 用于报告实际计数（测试输出里可见）
    console.info(
      `[safeSvg] figure 引用=${collected.length} 去重=${entries.length} max长度=${Math.max(...entries.map((e) => e.figure.length))}`,
    );
  });

  it('每一个真实 figure 都能被 parseSvgFigure 解析为 safe 且 nodes 非空', () => {
    const failures: { where: string; reason: string; sample: string }[] = [];
    for (const { where, figure } of entries) {
      const parsed = parseSvgFigure(figure);
      if (parsed === null) {
        failures.push({ where, reason: 'parseSvgFigure 返回 null', sample: figure.slice(0, 200) });
        continue;
      }
      if (!parsed.safe) {
        failures.push({ where, reason: 'safe=false', sample: figure.slice(0, 200) });
        continue;
      }
      if (parsed.nodes.length === 0) {
        failures.push({ where, reason: 'nodes 为空', sample: figure.slice(0, 200) });
      }
      if (!isSafeSvg(figure)) {
        failures.push({ where, reason: 'isSafeSvg=false', sample: figure.slice(0, 200) });
      }
    }
    expect(failures).toEqual([]);
  });

  it('每一个真实 figure 的净化树不含禁止标签 / on* / 外链 href', () => {
    const violations: string[] = [];
    for (const { where, figure } of entries) {
      const parsed = parseSvgFigure(figure);
      if (!parsed || !parsed.safe) continue; // 上一条用例已报
      for (const el of collectElements(parsed.nodes)) {
        const tag = el.tag.toLowerCase();
        if (FORBIDDEN_TAGS.has(tag)) violations.push(`${where}: 禁止标签 <${tag}>`);
        if (!EXPECTED_TAGS.has(tag)) violations.push(`${where}: 非预期标签 <${tag}>（可能被静默丢弃）`);
        for (const [k, v] of Object.entries(el.attrs)) {
          const lower = k.toLowerCase();
          if (lower.startsWith('on')) violations.push(`${where}: 事件属性 ${k}`);
          if (lower === 'href' || lower === 'xlink:href' || lower === 'src' || lower === 'style') {
            violations.push(`${where}: 外链/样式属性 ${k}`);
          }
          if (lower.includes('href') && !v.startsWith('#')) violations.push(`${where}: 外部引用 ${k}=${v}`);
          if (/(^|[^#])url\s*\(/i.test(v) && !/url\s*\(\s*#/i.test(v)) {
            violations.push(`${where}: 非本地 url() 引用 ${k}=${v.slice(0, 80)}`);
          }
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it('整批遍历耗时有界（DoS 防护）', () => {
    const t0 = performance.now();
    for (const { figure } of entries) parseSvgFigure(figure);
    const elapsed = performance.now() - t0;
    console.info(`[safeSvg] 全量 ${entries.length} 个去重 figure 解析耗时 ${elapsed.toFixed(0)}ms`);
    expect(elapsed).toBeLessThan(30_000);
  });
});

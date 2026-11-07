// 不可信内联 SVG 的安全解析与净化（卡 TX-FE-CODER）。
//
// 题面 figure（数量以内置题库数据文件为准）/选项 figure 是上游数据里的原始
// SVG 字符串，属于不可信
// 内容：绝不 dangerouslySetInnerHTML 原文注入。这里用 DOMParser 解析成文档后按
// 白名单重建为 React 元素——标签白名单 + 属性白名单双闸：
//   * 标签白名单拒绝 script / foreignObject / image / iframe / use 等一切可执行
//     或可外联的元素（连 <animate attributeName="href"> 这类注入向量也一并挡掉：
//     白名单里没有的标签整棵子树丢弃）；
//   * 属性白名单只留纯几何/绘制属性，on* 事件属性、href/xlink:href、style 里的
//     url(...) 一律不收；
//   * 文本节点原样保留（textContent 语义，React 转义）。
//
// parseSvgFigure 返回结构化节点树（纯数据，可测、可序列化）；<SafeFigure> 把它
// 渲染为 React 元素。任何解析失败返回 null——一道题渲染不出图也必须能读到题面。

/** 支持的 SVG 形状/结构标签（白名单之外的标签整棵丢弃）。 */
const TAG_ALLOWLIST = new Set([
  'svg', 'g', 'defs', 'title', 'desc',
  'path', 'rect', 'circle', 'ellipse', 'line', 'polyline', 'polygon',
  'text', 'tspan', 'textPath',
  'marker', 'clipPath', 'mask', 'pattern', 'linearGradient', 'radialGradient', 'stop', 'filter',
]);

/** 允许的属性名单（几何与绘制属性；白名单之外的属性丢弃）。
 *  键一律小写——检查时属性名先 toLowerCase 再比对（源数据大小写不可信），
 *  存储时保留原始写法（SVG attribute 大小写敏感，viewBox ≠ viewbox）。 */
const ATTR_ALLOWLIST = new Set([
  // svg 根
  'viewbox', 'xmlns', 'width', 'height', 'preserveaspectratio', 'role', 'aria-hidden', 'aria-label', 'focusable',
  // 分组与坐标
  'id', 'transform', 'x', 'y', 'dx', 'dy', 'x1', 'x2', 'y1', 'y2', 'cx', 'cy', 'r', 'rx', 'ry',
  'width', 'height', 'points', 'd',
  // 描边填充
  'fill', 'fill-opacity', 'fill-rule', 'stroke', 'stroke-width', 'stroke-opacity', 'stroke-linecap',
  'stroke-linejoin', 'stroke-dasharray', 'stroke-dashoffset', 'stroke-miterlimit', 'opacity',
  'color', 'font-family', 'font-size', 'font-style', 'font-weight', 'text-anchor',
  'dominant-baseline', 'letter-spacing', 'word-spacing', 'writing-mode',
  'offset', 'stop-color', 'stop-opacity',
  // 渐变/裁剪引用（仅同文档 id 引用；引用目标本身也在白名单标签内）
  'gradientunits', 'gradienttransform', 'patternunits', 'patterncontentunits',
  'clippathunits', 'maskunits', 'maskcontentunits', 'markerwidth', 'markerheight',
  'refx', 'refy', 'orient', 'markerunits', 'clip-path', 'mask', 'filter',
  'spreadmethod', 'fx', 'fy', 'patterntransform',
]);

/** 危险标签（第二道防线：即使有人扩了白名单，这些也显式拒绝并整树判非法）。 */
const TAG_DENYLIST = new Set([
  'script', 'foreignobject', 'image', 'iframe', 'use', 'animate', 'animatetransform',
  'animatemotion', 'set', 'handler', 'listener', 'a',
]);

/** 解析后的净化节点（纯数据）。 */
export type SvgNode =
  | { kind: 'element'; tag: string; attrs: Record<string, string>; children: SvgNode[] }
  | { kind: 'text'; text: string };

/** 解析结果：根节点数组（可能是多个 svg 或解析失败）。 */
export interface ParsedFigure {
  nodes: SvgNode[];
  /** false = 输入含危险标签，整段拒绝（不渲染任何内容）。 */
  safe: boolean;
}

/**
 * 净化递归的深度上限（从 svg 根往下数）。
 * 依据：内置题库 408 个唯一 figure 的真实嵌套深度远小于此值（手绘几何图形），
 * 64 足以容纳一切合法题目图形；而攻击者可用数万层嵌套把无界递归打成栈溢出
 * （DEF-SVG-03 实测 5000 层 <g> 即抛 RangeError）。超限即停止深入并整段判
 * unsafe——渲染不出图可以降级读题面，但绝不因深嵌套崩溃或挂死。
 */
const MAX_DEPTH = 64;

/** 粗检：包含危险标签即整段拒绝（先挡在解析前，省得白名单漏一条）。
 *  只扫标签形态、不扫 javascript:/data:text\/html 等内容形态：这类字符串会合法
 *  出现在 <style> 文本或普通文本节点里（DEF-SVG-02 实证：整串扫描把含
 *  <style> 的安全 figure 误判成整段拒绝）。内容级检查由属性值 RE_ATTR_UNSAFE
 *  承担——文本节点/注释/被丢弃子树经白名单重建后永不成为可执行内容。 */
const RE_UNSAFE = /<\s*script|<\s*foreignobject|<\s*image|<\s*iframe|<\s*use\b|<\s*animate|<\s*animatetransform|<\s*animatemotion|<\s*set\b|<\s*a\b|<\s*handler|<\s*listener/i;

/** 拒绝列表里的属性（第二道防线，防有人扩 ATTR_ALLOWLIST 时误放行）。 */
const ATTR_DENY_PATTERN = /^on/i;
const ATTR_DENYLIST = new Set(['href', 'xlink:href', 'xlink:title', 'style', 'src', 'formaction', 'from', 'to', 'values']);

/** 属性值危险内容检查（url(...) / javascript: / entity 注入）。 */
const RE_ATTR_UNSAFE = /javascript:|data:text\/html|url\s*\(/i;

function isAllowedAttr(name: string): boolean {
  const lower = name.toLowerCase();
  if (ATTR_DENYLIST.has(lower)) return false;
  if (ATTR_DENY_PATTERN.test(lower)) return false;
  if (!ATTR_ALLOWLIST.has(lower)) return false;
  return true;
}

/** 静默丢弃的纯文本/元数据标签：连同子树一起丢，但不置 unsafe（兄弟安全节点照常保留）。
 *  <style> 的文本子节点（@import / expression() / url(javascript:)）随元素一起
 *  丢弃即可，不能走到属性值 RE_ATTR_UNSAFE 检查——那会把安全兄弟节点一起误伤成
 *  整段拒绝（DEF-SVG-02 根因）。 */
const TAG_DROP_SUBTREE = new Set(['style']);

function sanitizeElement(
  el: Element,
  out: SvgNode[],
  unsafeRef: { unsafe: boolean },
  depth: number,
): void {
  const tag = el.tagName.toLowerCase();
  if (TAG_DENYLIST.has(tag)) {
    // 送到这里说明粗检没拦住（如大小写混淆）：按不可信处理。
    unsafeRef.unsafe = true;
    return;
  }
  // 超过嵌套深度上限：停止深入并整段判非法（防止恶意深嵌套打爆递归栈，DEF-SVG-03）。
  if (depth >= MAX_DEPTH) {
    unsafeRef.unsafe = true;
    return;
  }
  // 纯文本/元数据标签（如 <style>）：静默丢弃该元素及其子树，不污染兄弟节点。
  if (TAG_DROP_SUBTREE.has(tag)) return;
  if (!TAG_ALLOWLIST.has(tag)) return; // 未知标签：丢弃该元素（保留其安全子节点不必要——题目图形语义上丢一个未知元素比放行安全）
  const attrs: Record<string, string> = {};
  for (let i = 0; i < el.attributes.length; i++) {
    const attr = el.attributes[i];
    const lower = attr.name.toLowerCase();
    if (!isAllowedAttr(lower)) continue;
    if (RE_ATTR_UNSAFE.test(attr.value)) {
      unsafeRef.unsafe = true;
      continue;
    }
    // 保留源属性原始大小写（viewBox 等 SVG attribute 大小写敏感）。
    attrs[attr.name] = attr.value;
  }
  const children: SvgNode[] = [];
  collectChildren(el, children, unsafeRef, depth + 1);
  out.push({ kind: 'element', tag, attrs, children });
}

function collectChildren(
  node: Node,
  out: SvgNode[],
  unsafeRef: { unsafe: boolean },
  depth: number,
): void {
  for (let i = 0; i < node.childNodes.length; i++) {
    const child = node.childNodes[i];
    if (child.nodeType === 3 /* TEXT_NODE */) {
      // jsdom 对 image/svg+xml 会把纯文本解析成 parsererror；text/html 才保留
      // 文本节点。统一用 text/html 解析（DOMParser 不执行脚本，两个 MIME 都安全）。
      const text = child.nodeValue ?? '';
      if (text.trim()) out.push({ kind: 'text', text });
      continue;
    }
    if (child.nodeType === 1 /* ELEMENT_NODE */) {
      sanitizeElement(child as Element, out, unsafeRef, depth);
    }
    // 注释/CDATA/PI：丢弃。
  }
}

/**
 * 解析并净化一段内联 SVG。输入必须以 <svg 开头（题库数据约定）；
 * 含危险标签/属性时 safe=false（调用方应整段不渲染）。
 * DOMParser 不执行脚本，解析本身安全；jsdom/node 均支持。
 * 递归深度受 MAX_DEPTH 约束（超限整段拒绝，不抛错）。
 */
export function parseSvgFigure(input: string | null | undefined): ParsedFigure | null {
  if (!input || typeof input !== 'string') return null;
  const trimmed = input.trim();
  if (!trimmed.startsWith('<svg')) return null;
  if (RE_UNSAFE.test(trimmed)) return { nodes: [], safe: false };
  if (typeof DOMParser === 'undefined') return null;
  let doc: Document;
  try {
    // text/html：SVG 在 HTML 文档里按规范解析（文本节点保留），DOMParser
    // 同样不执行脚本；image/svg+xml 在部分实现里把纯文本判为 parsererror。
    doc = new DOMParser().parseFromString(trimmed, 'text/html');
  } catch {
    return null;
  }
  const parseErrors = doc.getElementsByTagName('parsererror');
  if (parseErrors.length > 0) return null;
  // text/html 下根是 <html>，svg 在 body 里；取文档内全部 svg 根（题库约定单根）。
  const roots = Array.from(doc.getElementsByTagName('svg'));
  if (roots.length === 0) return null;
  const unsafeRef = { unsafe: false };
  const nodes: SvgNode[] = [];
  for (const root of roots) {
    sanitizeElement(root, nodes, unsafeRef, 0);
  }
  if (unsafeRef.unsafe) return { nodes: [], safe: false };
  return { nodes, safe: true };
}

/** 判断一段 SVG 是否完全安全（渲染前的便捷谓词）。 */
export function isSafeSvg(input: string | null | undefined): boolean {
  const parsed = parseSvgFigure(input);
  return parsed !== null && parsed.safe && parsed.nodes.length > 0;
}

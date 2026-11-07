// 净化后的 SVG 题图渲染组件（卡 TX-FE-CODER）。
//
// parseSvgFigure 的输出是纯数据节点树；这里重建为 React 元素。没有任何
// dangerouslySetInnerHTML：属性走 React 的 SVG 属性系统，文本节点由 React
// 转义。不安全的输入渲染提示占位（图没画出来，但题面仍可读）。

import React from 'react';
import { parseSvgFigure, type SvgNode } from './safeSvg';
import s from './workspace.module.css';

/** 渲染一棵净化节点树。 */
function renderNode(node: SvgNode, key: React.Key): React.ReactNode {
  if (node.kind === 'text') {
    return <React.Fragment key={key}>{node.text}</React.Fragment>;
  }
  const children = node.children.map((child, i) => renderNode(child, i));
  return React.createElement(node.tag, { key, ...node.attrs }, children.length ? children : undefined);
}

export interface SafeFigureProps {
  /** 原始（不可信）内联 SVG 字符串。 */
  svg: string | null | undefined;
  /** 无障碍名称（题图的角色是 img）。 */
  label?: string;
  className?: string;
  /** 危险内容被拒绝时的提示文案；传空串则不渲染提示。 */
  blockedHint?: string;
}

/**
 * 题面/选项的内联 SVG 图：先净化再渲染。非法或危险的输入绝不注入 DOM，
 * 只显示占位提示——渲染不出图不影响读题。
 */
export const SafeFigure: React.FC<SafeFigureProps> = ({
  svg,
  label,
  className,
  blockedHint = '配图未能安全显示',
}) => {
  const parsed = parseSvgFigure(svg);
  if (!parsed || !parsed.safe || parsed.nodes.length === 0) {
    // 静默降级：很多题没有 figure，非法 SVG 才显示提示。
    if (svg && parsed && !parsed.safe && blockedHint) {
      return (
        <div className={`${s.figureBlocked} ${className ?? ''}`} data-test="figure-blocked" role="note">
          {blockedHint}
        </div>
      );
    }
    return null;
  }
  return (
    <div
      className={`${s.figure} ${className ?? ''}`}
      data-test="safe-figure"
      role={label ? 'img' : undefined}
      aria-label={label}
    >
      {parsed.nodes.map((node, i) => renderNode(node, i))}
    </div>
  );
};

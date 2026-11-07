// @vitest-environment jsdom
// SafeFigure 组件测试（卡 TX-FE-CODER）：净化后渲染成 React 元素，
// 危险输入绝不进入 DOM（无 script 节点、无 on* 属性、无外链）。

import { describe, expect, it } from 'vitest';
import { render } from '@testing-library/react';
import { SafeFigure } from './SafeFigure';
import './testUtils'; // data-test 作为 testId 查询属性（仓库约定）

const goodSvg =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><path d="M 0 0 L 10 10" fill="none" stroke="currentColor"/></svg>';

describe('SafeFigure', () => {
  it('合法 SVG 渲染为 svg 元素（无 dangerouslySetInnerHTML）', () => {
    const { container } = render(<SafeFigure svg={goodSvg} label="题目配图" />);
    const host = container.querySelector('[data-test="safe-figure"]');
    expect(host).not.toBeNull();
    const svg = host!.querySelector('svg');
    expect(svg).not.toBeNull();
    expect(svg!.querySelector('path')).not.toBeNull();
    expect(host!.getAttribute('role')).toBe('img');
    expect(host!.getAttribute('aria-label')).toBe('题目配图');
  });

  it('危险 SVG 显示拒绝占位而不是注入', () => {
    const { container } = render(
      <SafeFigure svg={'<svg><script>alert(1)</script></svg>'} />,
    );
    expect(container.querySelector('script')).toBeNull();
    // 组件用 data-test（仓库约定），不是 data-testid。
    expect(container.querySelector('[data-test="figure-blocked"]')).not.toBeNull();
  });

  it('事件属性不渲染到 DOM', () => {
    const { container } = render(
      <SafeFigure svg={'<svg><circle cx="1" cy="1" r="1" onclick="steal()"/></svg>'} />,
    );
    expect(container.querySelector('[onclick]')).toBeNull();
    expect(container.querySelector('[data-test="safe-figure"] circle')).not.toBeNull();
  });

  it('空/undefined/非 svg 字符串渲染 null', () => {
    const { container } = render(
      <>
        <SafeFigure svg={null} />
        <SafeFigure svg={undefined} />
        <SafeFigure svg="" />
        <SafeFigure svg="plain text" />
      </>,
    );
    expect(container.querySelector('[data-test="safe-figure"]')).toBeNull();
    expect(container.querySelector('[data-test="figure-blocked"]')).toBeNull();
  });

  it('文本内容转义渲染（不产生脚本可执行内容）', () => {
    const { container } = render(
      <SafeFigure svg={'<svg><text>alert(1)</text></svg>'} />,
    );
    expect(container.querySelector('script')).toBeNull();
    expect(container.textContent).toContain('alert(1)'); // 作为文本，不执行
  });
});

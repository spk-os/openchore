// @vitest-environment jsdom
// ShareCard 组件测试（卡 TX-FE-FIX2 / DEF-17）：分享链接卡展示、
// 路由页 URL 拼装（不把 API 端点当网页）、复制/关闭交互、二维码本地渲染
//（qrcode.react lazy 拆包，mock 掉避免 jsdom canvas 开销）。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const { QRCodeCanvas } = vi.hoisted(() => ({
  QRCodeCanvas: vi.fn((props: { value?: string }) => (
    <canvas data-test="tx-share-qr" data-value={props.value} />
  )),
}));
vi.mock('qrcode.react', () => ({ QRCodeCanvas }));

import { ShareCard } from './ShareCard';
import './testUtils'; // data-test 作为 testId 查询属性（仓库约定）

beforeEach(() => {
  vi.stubGlobal('location', { origin: 'http://localhost:5173' });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const result = {
  share_id: 9,
  url: '/share/taxonomy/abc123',
  token: 'abc123',
  expires_at: '2026-11-12T00:00:00Z',
};

describe('ShareCard（分享链接卡 + 本地二维码）', () => {
  it('展示路由页 URL（不是 API 端点）且二维码编码同一 URL', async () => {
    render(<ShareCard result={result} onClose={() => {}} />);
    await waitFor(() => {
      expect(screen.getByTestId('tx-share-card')).toBeTruthy();
    });
    const pageUrl = 'http://localhost:5173/share/taxonomy/abc123';
    // 展示的 URL 是前端路由页，不是 /api/taxonomy/shared/…。
    expect(screen.getByTestId('tx-share-url').textContent).toBe(pageUrl);
    expect(screen.queryByText(/\/api\/taxonomy\/shared\//)).toBeNull();
    // 二维码编码的也是路由页 URL。
    await waitFor(() => {
      expect(screen.getByTestId('tx-share-qr')).toBeTruthy();
    });
    expect(screen.getByTestId('tx-share-qr').getAttribute('data-value')).toBe(pageUrl);
  });

  it('复制走 clipboard.writeText（成功后按钮反馈「已复制」）', async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    render(<ShareCard result={result} onClose={() => {}} />);
    fireEvent.click(screen.getByTestId('tx-share-copy'));
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith('http://localhost:5173/share/taxonomy/abc123');
    });
    await waitFor(() => {
      expect(screen.getByTestId('tx-share-copy').textContent).toBe('已复制');
    });
  });

  it('clipboard 不可用时显示复制错误行', async () => {
    vi.stubGlobal('navigator', {
      clipboard: { writeText: async () => { throw new Error('denied'); } },
    });
    render(<ShareCard result={result} onClose={() => {}} />);
    fireEvent.click(screen.getByTestId('tx-share-copy'));
    await waitFor(() => {
      expect(screen.getByTestId('tx-share-copy-error')).toBeTruthy();
    });
  });

  it('「收好链接」触发 onClose', () => {
    const onClose = vi.fn();
    render(<ShareCard result={result} onClose={onClose} />);
    fireEvent.click(screen.getByTestId('tx-share-close'));
    expect(onClose).toHaveBeenCalledOnce();
  });
});

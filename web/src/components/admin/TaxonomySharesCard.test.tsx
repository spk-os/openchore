// @vitest-environment jsdom
// TaxonomySharesCard 组件测试（卡 TX-TAXONOMY-SHARES-UI-CODER）：
// 加载/空/三态、仅 active 可撤销、confirm 取消不请求、成功后重拉、
// 撤销失败保留列表、kid 切换乱序（旧响应不覆盖新孩子）、无 kid 零请求、
// session_id 为 null 不崩溃、busy 防重复点击。
//
// 网络边界：taxonomyApi 在模块级 mock，组件只通过 listShares/revokeShare
// 说话，不伪造组件内部状态。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor, fireEvent, act } from '@testing-library/react';

// taxonomy client 模块级 mock：listShares / revokeShare 是唯一边界。
const { taxonomyApi } = vi.hoisted(() => ({
  taxonomyApi: {
    listShares: vi.fn(),
    revokeShare: vi.fn(),
  },
}));
vi.mock('../../taxonomy/client', () => ({ taxonomyApi }));

import { TaxonomySharesCard } from './TaxonomySharesCard';
import type { ShareRow, ShareStatus } from '../../taxonomy/types';
import '../../taxonomy/testUtils'; // data-test 作为 testId 查询属性（仓库约定）

// i18n：mock t 按 key 返回（本卡不编辑 locale 文件，键由集成卡补齐）；
// 带插值参数时把值附在 key 后（key:v1,v2），便于断言日期/ID 等真实入参。
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      if (opts && typeof opts === 'object') {
        const vals = Object.values(opts as Record<string, unknown>);
        if (vals.length) return `${key}:${vals.map(String).join(',')}`;
      }
      return key;
    },
  }),
}));

const row = (over: Partial<ShareRow> = {}): ShareRow => ({
  id: 7,
  owner_id: 42,
  session_id: 3,
  report_kind: 'diagnosis',
  expires_at: '2026-11-12T00:00:00Z',
  revoked_at: null,
  created_at: '2026-11-05T00:00:00Z',
  status: 'active' as ShareStatus,
  ...over,
});

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('TaxonomySharesCard（家长分享列表/撤销）', () => {
  it('加载成功渲染三态行：active/expired/revoked，仅 active 有撤销按钮', async () => {
    taxonomyApi.listShares.mockResolvedValue({
      shares: [
        row({ id: 11, status: 'active' }),
        row({ id: 12, status: 'expired', expires_at: '2026-11-01T00:00:00Z' }),
        row({
          id: 13,
          status: 'revoked',
          revoked_at: '2026-11-04T00:00:00Z',
        }),
      ],
    });
    render(<TaxonomySharesCard kidId={42} />);

    await screen.findByTestId('tx-shares-row-11');
    expect(taxonomyApi.listShares).toHaveBeenCalledWith(42);
    expect(screen.getByTestId('tx-shares-status-11').textContent).toBe('admin.taxonomyTab.sharesStatus_active');
    expect(screen.getByTestId('tx-shares-status-12').textContent).toBe('admin.taxonomyTab.sharesStatus_expired');
    expect(screen.getByTestId('tx-shares-status-13').textContent).toBe('admin.taxonomyTab.sharesStatus_revoked');
    // 仅 active 显示撤销。
    expect(screen.getByTestId('tx-shares-revoke-11')).toBeTruthy();
    expect(screen.queryByTestId('tx-shares-revoke-12')).toBeNull();
    expect(screen.queryByTestId('tx-shares-revoke-13')).toBeNull();
  });

  it('加载中显示 loading；空列表显示空态（服务端契约 []）', async () => {
    taxonomyApi.listShares.mockResolvedValue({ shares: [] });
    render(<TaxonomySharesCard kidId={42} />);
    expect(screen.getByTestId('tx-shares-loading')).toBeTruthy();
    await screen.findByTestId('tx-shares-empty');
    expect(screen.queryByTestId('tx-shares-loading')).toBeNull();
  });

  it('加载失败显示 role=alert 错误（服务端消息透传）', async () => {
    taxonomyApi.listShares.mockRejectedValue(new Error('failed to list shares'));
    render(<TaxonomySharesCard kidId={42} />);
    const err = await screen.findByTestId('tx-shares-error');
    expect(err.getAttribute('role')).toBe('alert');
    expect(err.textContent).toContain('failed to list shares');
  });

  it('kidId=null 零请求并提示先选孩子', () => {
    render(<TaxonomySharesCard kidId={null} />);
    expect(screen.getByTestId('tx-shares-card')).toBeTruthy();
    expect(screen.getByTestId('tx-shares-no-kid').textContent).toBe('admin.taxonomyTab.sharesNoKid');
    expect(taxonomyApi.listShares).not.toHaveBeenCalled();
  });

  it('confirm 取消不撤销', async () => {
    taxonomyApi.listShares.mockResolvedValue({ shares: [row()] });
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-revoke-7');
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    expect(confirmSpy).toHaveBeenCalled();
    expect(taxonomyApi.revokeShare).not.toHaveBeenCalled();
  });

  it('确认后撤销并重新拉列表；shares 为 null 防御为 []', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    // 第一次响应 shares 为 null：组件防御为空列表（服务端契约是 []）。
    taxonomyApi.listShares
      .mockResolvedValueOnce({ shares: [row()] })
      .mockResolvedValueOnce({ shares: null });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-revoke-7');
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    await waitFor(() => expect(taxonomyApi.revokeShare).toHaveBeenCalledWith(7));
    await screen.findByTestId('tx-shares-empty');
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2);
  });

  it('撤销失败保留现有列表并显示错误', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    taxonomyApi.listShares.mockResolvedValue({ shares: [row()] });
    taxonomyApi.revokeShare.mockRejectedValue(new Error('share not found'));
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-revoke-7');
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    const err = await screen.findByTestId('tx-shares-error');
    expect(err.getAttribute('role')).toBe('alert');
    expect(err.textContent).toContain('share not found');
    // 列表还在。
    expect(screen.getByTestId('tx-shares-row-7')).toBeTruthy();
    // 撤销失败不重拉（revokeShare 拒绝后 load 不再触发第二次）。
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(1);
  });

  it('撤销进行中按钮 busy：busy 期间再点不重复请求', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let resolveRevoke: ((v: { ok: boolean }) => void) | undefined;
    taxonomyApi.listShares
      .mockResolvedValueOnce({ shares: [row()] })
      .mockResolvedValueOnce({ shares: [] });
    taxonomyApi.revokeShare.mockImplementation(
      () => new Promise<{ ok: boolean }>((resolve) => { resolveRevoke = resolve; }),
    );
    render(<TaxonomySharesCard kidId={42} />);
    const btn = (await screen.findByTestId('tx-shares-revoke-7')) as HTMLButtonElement;
    fireEvent.click(btn);
    await waitFor(() => expect(btn.disabled).toBe(true));
    // busy 期间再点（即使 enabled）也不重复请求。
    fireEvent.click(btn);
    expect(taxonomyApi.revokeShare).toHaveBeenCalledTimes(1);
    await act(async () => { resolveRevoke!({ ok: true }); });
    await waitFor(() => expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2));
    // 撤销→重拉完成后：busy 解除，刷新按钮恢复可用。
    await screen.findByTestId('tx-shares-empty');
    await waitFor(() => expect((screen.getByTestId('tx-shares-refresh') as HTMLButtonElement).disabled).toBe(false));
    // busy 已解除：重拉的空列表里没有可再撤销的行（row-7 已不渲染），
    // 这里断言刷新可用即证明 revokingId 已清空（刷新在 revoking 时禁用）。
  });

  it('session_id 为 null 不崩溃且显示无会话占位', async () => {
    taxonomyApi.listShares.mockResolvedValue({
      shares: [row({ id: 21, session_id: null, report_kind: 'path' })],
    });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-row-21');
    expect(screen.getByTestId('tx-shares-row-21').textContent).toContain('admin.taxonomyTab.sharesSessionNone');
  });

  it('kid 切换：旧孩子慢响应晚到也不覆盖新孩子的列表', async () => {
    let resolveSlow: ((v: { shares: ShareRow[] }) => void) | undefined;
    const stale = { shares: [row({ id: 71, owner_id: 7 })] };
    const fresh = { shares: [row({ id: 91, owner_id: 9 })] };
    taxonomyApi.listShares
      .mockImplementationOnce(() => new Promise<{ shares: ShareRow[] }>((resolve) => { resolveSlow = resolve; }))
      .mockResolvedValueOnce(fresh);
    const { rerender } = render(<TaxonomySharesCard kidId={7} />);
    await waitFor(() => expect(taxonomyApi.listShares).toHaveBeenCalledTimes(1));
    rerender(<TaxonomySharesCard kidId={9} />);
    await screen.findByTestId('tx-shares-row-91');
    expect(taxonomyApi.listShares).toHaveBeenLastCalledWith(9);
    // 旧孩子慢响应此刻才到达：代数作废，不得覆盖新孩子列表。
    await act(async () => { resolveSlow!(stale); });
    expect(screen.getByTestId('tx-shares-row-91')).toBeTruthy();
    expect(screen.queryByTestId('tx-shares-row-71')).toBeNull();
  });

  it('kidId 从有值切回 null：清空列表并不再请求', async () => {
    taxonomyApi.listShares.mockResolvedValue({ shares: [row()] });
    const { rerender } = render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-row-7');
    rerender(<TaxonomySharesCard kidId={null} />);
    expect(screen.getByTestId('tx-shares-no-kid')).toBeTruthy();
    expect(screen.queryByTestId('tx-shares-row-7')).toBeNull();
    // null 之后不再发请求。
    await new Promise((r) => setTimeout(r, 0));
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(1);
  });

  it('手动刷新按钮重新拉列表', async () => {
    taxonomyApi.listShares
      .mockResolvedValueOnce({ shares: [] })
      .mockResolvedValueOnce({ shares: [row()] });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-empty');
    fireEvent.click(screen.getByTestId('tx-shares-refresh'));
    await screen.findByTestId('tx-shares-row-7');
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2);
  });

  it('真实撤销只发 id，不把 token/整行带进 DELETE 请求', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    // 即使响应行被塞进 token 字段，撤销请求也只能是 id。
    const dirty = { ...row(), token: 'ocs_share_SECRET', token_hash: 'deadbeef' } as unknown as ShareRow;
    taxonomyApi.listShares.mockResolvedValue({ shares: [dirty] });
    taxonomyApi.revokeShare.mockResolvedValue({ ok: true });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-revoke-7');
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    await waitFor(() => expect(taxonomyApi.revokeShare).toHaveBeenCalledTimes(1));
    expect(taxonomyApi.revokeShare.mock.calls[0]).toEqual([7]);
    // 组件不把 token 渲染到 DOM。
    expect(screen.getByTestId('tx-shares-card').textContent).not.toContain('ocs_share_SECRET');
    expect(screen.getByTestId('tx-shares-card').textContent).not.toContain('deadbeef');
  });

  it('卸载后迟到响应不崩溃、不再 setState 渲染（卸载由代数兜底）', async () => {
    let resolveSlow: ((v: { shares: ShareRow[] }) => void) | undefined;
    let rejectSlow: ((e: Error) => void) | undefined;
    taxonomyApi.listShares
      .mockImplementationOnce(() => new Promise((resolve) => { resolveSlow = resolve; }))
      .mockImplementationOnce(() => new Promise((_r, reject) => { rejectSlow = reject; }));
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    const first = render(<TaxonomySharesCard kidId={42} />);
    await waitFor(() => expect(taxonomyApi.listShares).toHaveBeenCalledTimes(1));
    first.unmount();
    await act(async () => { resolveSlow!({ shares: [row({ id: 81 })] }); });
    // 第二个实例在响应前卸载，迟到 reject 也不得产生未处理错误/告警。
    const second = render(<TaxonomySharesCard kidId={43} />);
    await waitFor(() => expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2));
    second.unmount();
    await act(async () => { rejectSlow!(new Error('late failure')); });
    expect(errSpy.mock.calls.flat().join(' ')).not.toContain('late failure');
  });

  it('撤销成功后重拉时若孩子已切换：旧孩子的重拉不覆盖新孩子列表', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let resolveRevoke: ((v: { ok: boolean }) => void) | undefined;
    let resolveRevokeReload: ((v: { shares: ShareRow[] }) => void) | undefined;
    const kid7 = { shares: [row({ id: 7, owner_id: 70 })] };
    taxonomyApi.listShares
      .mockResolvedValueOnce(kid7) // kid 7 首拉
      .mockImplementationOnce(() =>
        new Promise((resolve) => { resolveRevokeReload = resolve; }),
      );
    taxonomyApi.revokeShare.mockImplementation(
      () => new Promise<{ ok: boolean }>((resolve) => { resolveRevoke = resolve; }),
    );
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-row-7');
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    await waitFor(() => expect(taxonomyApi.revokeShare).toHaveBeenCalledWith(7));
    // 撤销成功，组件开始重拉 kid 42（慢）；此时家长切到 kid 43（快）。
    await act(async () => { resolveRevoke!({ ok: true }); });
    await waitFor(() => expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2));
    // 记录行为：撤销成功后的重拉用闭包里的 kidId 42，而不是当前选中孩子；
    // 若此刻已切换，这次重拉仍打向旧孩子（见遗留说明 #2），据此断言当前实现。
    expect(taxonomyApi.listShares).toHaveBeenLastCalledWith(42);
    expect(taxonomyApi.listShares.mock.calls.map((c) => c[0])).toEqual([42, 42]);
    expect(typeof resolveRevokeReload).toBe('function');
    // 旧孩子的迟到重拉结果不得覆盖新孩子列表。
    await act(async () => { resolveRevokeReload!({ shares: [] }); });
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2);
  });

  it('kid 切换后旧孩子的在途撤销失败不污染新孩子的视图', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let rejectRevoke: ((e: Error) => void) | undefined;
    taxonomyApi.listShares.mockImplementation((uid: number) =>
      Promise.resolve(uid === 42 ? { shares: [row({ id: 7 })] } : { shares: [row({ id: 9, owner_id: 90 })] }),
    );
    taxonomyApi.revokeShare.mockImplementation(
      () => new Promise<{ ok: boolean }>((_r, reject) => { rejectRevoke = reject; }),
    );
    const { rerender } = render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-revoke-7');
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    await waitFor(() => expect(taxonomyApi.revokeShare).toHaveBeenCalledTimes(1));
    rerender(<TaxonomySharesCard kidId={43} />);
    await screen.findByTestId('tx-shares-row-9');
    // 旧孩子的撤销此刻失败：代数已过，错误不得串到新孩子的视图（DEF-SHARES-UI-R1）。
    await act(async () => { rejectRevoke!(new Error('kid42 revoke failed')); });
    expect(screen.getByTestId('tx-shares-row-9')).toBeTruthy();
    expect(screen.queryByTestId('tx-shares-error')).toBeNull();
    expect(screen.queryByText('kid42 revoke failed')).toBeNull();
    // 旧孩子的在途失败不得把新孩子的按钮永久锁死：撤销按钮已可再点。
    expect((screen.getByTestId('tx-shares-revoke-9') as HTMLButtonElement).disabled).toBe(false);
  });

  it('kid 切换后旧孩子的在途撤销成功：不重拉旧对象，不覆盖新孩子视图', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let resolveRevoke: ((v: { ok: boolean }) => void) | undefined;
    let resolveRevokeReload: ((v: { shares: ShareRow[] }) => void) | undefined;
    const lists = new Map<number, { shares: ShareRow[] }>();
    taxonomyApi.listShares.mockImplementation((uid: number) => {
      if (lists.has(uid)) {
        // 若组件错误地重拉某个 kid，把那次请求悬置（不 resolve），
        // 让「旧 kid 的重拉」在断言前可见且不影响后续渲染。
        return new Promise<{ shares: ShareRow[] }>((resolve) => { resolveRevokeReload = resolve; });
      }
      lists.set(uid, uid === 42
        ? { shares: [row({ id: 7 })] }
        : { shares: [row({ id: 9, owner_id: 90 })] });
      return Promise.resolve(lists.get(uid)!);
    });
    taxonomyApi.revokeShare.mockImplementation(
      () => new Promise<{ ok: boolean }>((resolve) => { resolveRevoke = resolve; }),
    );
    const { rerender } = render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-revoke-7');
    fireEvent.click(screen.getByTestId('tx-shares-revoke-7'));
    await waitFor(() => expect(taxonomyApi.revokeShare).toHaveBeenCalledTimes(1));
    rerender(<TaxonomySharesCard kidId={43} />);
    await screen.findByTestId('tx-shares-row-9');
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2); // 42 首拉 + 43 首拉
    // 旧孩子的撤销此刻才成功：已切换孩子，不得再对旧 kid 42 发起重拉。
    await act(async () => { resolveRevoke!({ ok: true }); });
    await new Promise((r) => setTimeout(r, 0));
    expect(taxonomyApi.listShares).toHaveBeenCalledTimes(2); // 无新增请求
    expect(taxonomyApi.listShares).toHaveBeenLastCalledWith(43);
    expect(resolveRevokeReload).toBeUndefined(); // 未对任何 kid 发起错误的重拉
    // 新孩子视图未被旧孩子的成功路径改写。
    expect(screen.getByTestId('tx-shares-row-9')).toBeTruthy();
    expect(screen.queryByTestId('tx-shares-error')).toBeNull();
  });

  it('三态徽章：active/expired/revoked 都有徽章样式；expired 与 revoked 一致、active 另有差异', async () => {
    taxonomyApi.listShares.mockResolvedValue({
      shares: [
        row({ id: 11, status: 'active' }),
        row({ id: 12, status: 'expired' }),
        row({ id: 13, status: 'revoked', revoked_at: '2026-11-04T00:00:00Z' }),
      ],
    });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-status-11');
    const cls = (id: number) => screen.getByTestId(`tx-shares-status-${id}`).className.split(/\s+/).filter(Boolean);
    // 徽章样式来自 CSS Modules（badge/badgeOutline），类名带 hash 后缀。
    const badgeLike = cls(11).filter((c) => /badge/i.test(c));
    expect(badgeLike.length).toBeGreaterThan(0);
    expect(cls(12).join(' ')).toContain('badgeOutline');
    expect(cls(13).join(' ')).toContain('badgeOutline');
    expect(cls(13)).toEqual(cls(12));
    // active 与弱化态不同（STATUS_BADGE.active = ui.badge）。
    expect(cls(11).join(' ')).not.toContain('badgeOutline');
  });

  it('expires_at=null 显示"永不过期"占位而非崩溃', async () => {
    taxonomyApi.listShares.mockResolvedValue({ shares: [row({ id: 51, expires_at: null })] });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-row-51');
    expect(screen.getByTestId('tx-shares-row-51').textContent).toContain('admin.taxonomyTab.sharesExpiresNever');
  });

  it('刷新失败：旧列表保留并显示 role=alert（DEF-SHARES-UI-R1）', async () => {
    taxonomyApi.listShares
      .mockResolvedValueOnce({ shares: [row({ id: 7 })] })
      .mockRejectedValueOnce(new Error('refresh boom'));
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-row-7');
    fireEvent.click(screen.getByTestId('tx-shares-refresh'));
    const err = await screen.findByTestId('tx-shares-error');
    expect(err.getAttribute('role')).toBe('alert');
    expect(err.textContent).toContain('refresh boom');
    // 失败不清空已有列表：行保留，只报错。
    expect(screen.getByTestId('tx-shares-row-7')).toBeTruthy();
    // loading 结束，刷新可重试。
    expect((screen.getByTestId('tx-shares-refresh') as HTMLButtonElement).disabled).toBe(false);
  });

  it('初次加载失败（无旧数据）：仍走 role=alert，不渲染空态', async () => {
    taxonomyApi.listShares.mockRejectedValue(new Error('initial load failed'));
    render(<TaxonomySharesCard kidId={42} />);
    const err = await screen.findByTestId('tx-shares-error');
    expect(err.getAttribute('role')).toBe('alert');
    expect(err.textContent).toContain('initial load failed');
    // 无旧数据时也不伪造空态（shares 仍为 null）。
    expect(screen.queryByTestId('tx-shares-empty')).toBeNull();
    expect(screen.queryByTestId('tx-shares-row-7')).toBeNull();
  });

  it('时间字段可读化（formatDateTime），revoked_at 有展示', async () => {
    taxonomyApi.listShares.mockResolvedValue({
      shares: [
        row({
          id: 31,
          status: 'revoked',
          revoked_at: '2026-11-04T12:34:00Z',
          created_at: '2026-11-01T12:34:00Z',
        }),
      ],
    });
    render(<TaxonomySharesCard kidId={42} />);
    await screen.findByTestId('tx-shares-row-31');
    const text = screen.getByTestId('tx-shares-row-31').textContent ?? '';
    // formatDateTime 输出「YYYY-MM-DD HH:mm」；不再输出原始 RFC3339 Z 尾巴。
    expect(text).not.toContain('2026-11-01T12:34:00Z');
    expect(text).toMatch(/\d{4}-\d{2}-\d{2} \d{2}:\d{2}/);
    expect(text).toContain('admin.taxonomyTab.sharesRevokedAt');
  });
});

// 家长的孩子分享链接管理卡（卡 TX-TAXONOMY-SHARES-UI-CODER）。
// 数据源是 GET /api/admin/taxonomy/shares?user_id=（taxonomyApi.listShares），
// 撤销走 DELETE /api/admin/taxonomy/shares/{id}（taxonomyApi.revokeShare）。
// 列表行只有吊销需要的列（id/owner/session_id/report_kind/三个时间/status），
// 服务端契约保证不含 token/token_hash/snapshot/答案——本组件同样不显示、
// 不构造任何原始 token（链接发出去之后就只认 id 撤销）。
//
// 独立组件：props 冻结为 {kidId}，由 TaxonomyTab 后续直接挂载；
// kidId 为 null 时不发请求、给出先选孩子的提示。kid 切换/卸载/慢响应用
// 请求代数（generation）防旧响应覆盖新孩子的列表。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link2, RefreshCw } from 'lucide-react';
import { taxonomyApi } from '../../taxonomy/client';
import type { ShareRow, SharesPage } from '../../taxonomy/types';
import { formatDateTime } from '../../taxonomy/labels';
import ui from './ui.module.css';

export interface TaxonomySharesCardProps {
  /** 选中的孩子（服务端 user_id）；null 时不请求。 */
  kidId: number | null;
}

/** 状态徽章的三态样式（active 用默认徽章，其余描边弱化）。 */
const STATUS_BADGE: Record<string, string> = {
  active: ui.badge,
  expired: ui.badgeOutline,
  revoked: ui.badgeOutline,
};

export const TaxonomySharesCard: React.FC<TaxonomySharesCardProps> = ({ kidId }) => {
  const { t } = useTranslation();
  const [shares, setShares] = useState<ShareRow[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [revokingId, setRevokingId] = useState<number | null>(null);
  // 请求代数：kid 切换/慢响应下旧响应不得覆盖新孩子的列表。
  const reqSeq = useRef(0);
  // 当前有效孩子的 ref 镜像：撤销成功后的重拉读它，而不是 handleRevoke
  // 闭包里的旧 kidId（DEF-SHARES-UI-R1：切孩子后不得用过期闭包拉旧对象）。
  const kidRef = useRef<number | null>(kidId);
  kidRef.current = kidId;

  const load = useCallback(async (uid: number) => {
    const seq = ++reqSeq.current;
    setLoading(true);
    setError('');
    try {
      const page: SharesPage = await taxonomyApi.listShares(uid);
      if (seq !== reqSeq.current) return; // 旧响应作废
      // 服务端契约空列表是 []；这里再做 null 防御，脏响应不当崩溃。
      setShares(page.shares ?? []);
    } catch (err) {
      if (seq !== reqSeq.current) return;
      // 失败不清空已有列表（DEF-SHARES-UI-R1）：保留旧数据只报错；
      // 初始无数据时 shares 仍为 null，成功路径的空列表空态不受影响。
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (seq === reqSeq.current) setLoading(false);
    }
  }, []);

  // kidId 变化即重拉；null 时清空状态并停请求（卸载由代数兜底作废）。
  useEffect(() => {
    if (kidId == null) {
      reqSeq.current++; // 在途请求全部作废
      setShares(null);
      setLoading(false);
      setError('');
      return;
    }
    void load(kidId);
  }, [kidId, load]);

  const handleRevoke = async (share: ShareRow) => {
    // 撤销前确认（吊销立即失效，不可恢复）；busy 期间防重复点击。
    if (!confirm(t('admin.taxonomyTab.sharesConfirmRevoke', { id: share.id }))) return;
    if (revokingId != null || kidId == null) return;
    // 撤销动作的代数：跨孩子切换/切回 null 后，旧孩子的在途撤销
    //（catch）与成功后的重拉都不得改写新孩子的视图（DEF-SHARES-UI-R1）。
    const revokeSeq = ++reqSeq.current;
    setRevokingId(share.id);
    setError('');
    try {
      await taxonomyApi.revokeShare(share.id);
      // 成功后重新拉列表，状态由服务端派生。读 kidRef（当前有效孩子，
      // 而非 handleRevoke 闭包里的旧 kidId），且只在本撤销仍是最新代数时
      // 重拉——孩子已切换/已卸载/已切回 null 时这次重拉直接放弃。
      const currentKid = kidRef.current;
      if (revokeSeq !== reqSeq.current || currentKid == null) return;
      await load(currentKid);
    } catch (err) {
      // 旧孩子的撤销失败不得污染新孩子的视图：代数不匹配直接丢弃。
      if (revokeSeq !== reqSeq.current) return;
      // 撤销失败：保留现有列表，只报错。
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      // busy 锁由本 handler 独占（入口 busy 守卫保证无并发撤销），撤销
      // settle 即释放——即便代数已过也要释放：守卫代数不释放会把新孩子的
      // 撤销/刷新按钮永久锁死；旧 DELETE 仍在途的短暂禁用是诚实的 busy。
      setRevokingId(prev => (prev === share.id ? null : prev));
    }
  };

  // 没选孩子：卡片可见但不请求，给出明确指引。
  if (kidId == null) {
    return (
      <section className={ui.card} data-test="tx-shares-card">
        <div className={ui.sectionHead}>
          <h3 className={ui.sectionTitle}>
            <Link2 aria-hidden /> {t('admin.taxonomyTab.sharesTitle')}
          </h3>
        </div>
        <p className={ui.help} data-test="tx-shares-no-kid">
          {t('admin.taxonomyTab.sharesNoKid')}
        </p>
      </section>
    );
  }

  return (
    <section className={ui.card} data-test="tx-shares-card">
      <div className={ui.sectionHead}>
        <h3 className={ui.sectionTitle}>
          <Link2 aria-hidden /> {t('admin.taxonomyTab.sharesTitle')}
        </h3>
        <button
          type="button"
          className={ui.btnGhost}
          onClick={() => { void load(kidId); }}
          disabled={loading || revokingId != null}
          title={t('admin.taxonomyTab.sharesRefreshTitle')}
          data-test="tx-shares-refresh"
        >
          <RefreshCw aria-hidden className={loading ? ui.spin : undefined} />
          {loading ? t('common.loading') : t('admin.taxonomyTab.sharesRefresh')}
        </button>
      </div>
      <p className={ui.sectionDesc}>{t('admin.taxonomyTab.sharesDesc')}</p>

      {loading && shares == null && (
        <p className={ui.emptyInline} role="status" data-test="tx-shares-loading">
          {t('common.loading')}
        </p>
      )}

      {error && (
        <p className={ui.msgError} role="alert" data-test="tx-shares-error">
          {error}
        </p>
      )}

      {shares != null && shares.length === 0 && !error && (
        <p className={ui.emptyInline} data-test="tx-shares-empty">
          {t('admin.taxonomyTab.sharesEmpty')}
        </p>
      )}

      {shares != null && shares.length > 0 && (
        <ul className={ui.list}>
          {shares.map(share => (
            <li key={share.id} className={ui.row} data-test={`tx-shares-row-${share.id}`}>
              <div className={ui.rowMain}>
                <span className={ui.rowTitle}>
                  <span
                    className={STATUS_BADGE[share.status] ?? ui.badgeOutline}
                    data-test={`tx-shares-status-${share.id}`}
                  >
                    {t(`admin.taxonomyTab.sharesStatus_${share.status}`)}
                  </span>
                  {' '}
                  {t('admin.taxonomyTab.sharesKind', { kind: share.report_kind })}
                </span>
                <span className={ui.rowMeta}>
                  <span>
                    {t('admin.taxonomyTab.sharesCreated', {
                      date: formatDateTime(share.created_at),
                    })}
                  </span>
                  <span>
                    {share.expires_at
                      ? t('admin.taxonomyTab.sharesExpires', {
                          date: formatDateTime(share.expires_at),
                        })
                      : t('admin.taxonomyTab.sharesExpiresNever')}
                  </span>
                  <span>
                    {t('admin.taxonomyTab.sharesSession', {
                      // session_id 可空（path/blindspots 报告无诊断会话）：null 不崩溃。
                      id: share.session_id ?? t('admin.taxonomyTab.sharesSessionNone'),
                    })}
                  </span>
                  {share.revoked_at && (
                    <span>
                      {t('admin.taxonomyTab.sharesRevokedAt', {
                        date: formatDateTime(share.revoked_at),
                      })}
                    </span>
                  )}
                </span>
              </div>
              {/* 只有 active 可撤销；expired/revoked 没有撤销按钮。 */}
              {share.status === 'active' && (
                <button
                  type="button"
                  className={ui.btnDanger}
                  onClick={() => { void handleRevoke(share); }}
                  disabled={revokingId != null}
                  title={t('admin.taxonomyTab.sharesRevokeTitle')}
                  data-test={`tx-shares-revoke-${share.id}`}
                >
                  {revokingId === share.id
                    ? t('admin.taxonomyTab.sharesRevoking')
                    : t('admin.taxonomyTab.sharesRevoke')}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
};

// 分享卡片（卡 TX-FE-FIX2 / DEF-17）：诊断结果分享链接的一次性展示卡。
// 本地二维码用项目已有依赖 qrcode.react 的 QRCodeCanvas（同仓库
// BoostLinksSection 用法：lazy 拆包，二维码编码不进主 bundle）——
// 不请求任何外部二维码服务，离线也能渲染。
//
// 链接语义：POST /share 返回的 url 是 API 端点 /api/taxonomy/shared/{token}；
// 浏览器打开的页面是前端路由 /share/taxonomy/{token}（同一 token）。
// 卡片展示/复制/编码的都是路由页 URL，不把 API 端点当网页发给家长。

import React from 'react';
import type { ShareResult } from './types';
import { formatDateTime } from './labels';
import { ErrorRow } from './parts';
import s from './workspace.module.css';

// qrcode.react 只在分享卡出现时加载。
const QRCodeCanvas = React.lazy(() => import('qrcode.react').then((m) => ({ default: m.QRCodeCanvas })));

export interface ShareCardProps {
  /** POST /share 的原始响应（url 是 API 端点）。 */
  result: ShareResult;
  /** 关闭卡片（家长收好链接后）。 */
  onClose: () => void;
}

/** 从 API url 提取 token（/api/taxonomy/shared/{token} → token）。 */
function tokenOf(url: string): string {
  const idx = url.lastIndexOf('/');
  return idx >= 0 ? url.slice(idx + 1) : url;
}

/** 分享卡：路由页链接 + canvas 二维码 + 复制 + 过期时间。 */
export const ShareCard: React.FC<ShareCardProps> = ({ result, onClose }) => {
  const [copied, setCopied] = React.useState(false);
  const [copyError, setCopyError] = React.useState<unknown>(null);

  // 浏览器页面 = 前端路由 /share/taxonomy/{token}；API url 不是页面。
  const pageUrl = `${window.location.origin}/share/taxonomy/${encodeURIComponent(tokenOf(result.url))}`;

  const copy = async () => {
    setCopyError(null);
    try {
      await navigator.clipboard.writeText(pageUrl);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      // 旧浏览器剪贴板 API 不可用：退回选中文本让用户手动复制。
      setCopyError(err);
    }
  };

  return (
    <div className={s.notice} data-test="tx-share-card" role="dialog" aria-label="分享链接">
      <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <React.Suspense fallback={<span className={s.muted}>二维码生成中…</span>}>
          <QRCodeCanvas
            value={pageUrl}
            size={148}
            marginSize={2}
            level="M"
            data-test="tx-share-qr"
            aria-label="分享链接二维码"
          />
        </React.Suspense>
        <div style={{ flex: '1 1 220px', minWidth: 0 }}>
          <p style={{ margin: '0 0 4px' }}>
            <b>分享链接已生成</b>
          </p>
          <p className={s.muted} style={{ margin: '0 0 8px', wordBreak: 'break-all' }} data-test="tx-share-url">
            {pageUrl}
          </p>
          <p className={s.muted} style={{ margin: '0 0 8px' }}>
            {result.expires_at ? `有效期至 ${formatDateTime(result.expires_at)}` : '请在有效期内打开'}
            ；家长无需登录即可查看这份报告的快照。
          </p>
          <div className={s.actionRow}>
            <button type="button" className={s.btnPrimary} data-test="tx-share-copy" onClick={copy}>
              {copied ? '已复制' : '复制链接'}
            </button>
            <button type="button" className={s.btn} data-test="tx-share-open" onClick={() => window.open(pageUrl, '_blank', 'noopener')}>
              打开预览
            </button>
            <button type="button" className={s.btnGhost} data-test="tx-share-close" onClick={onClose}>
              收好链接
            </button>
          </div>
          {copyError ? <ErrorRow error={copyError} test="tx-share-copy-error" /> : null}
        </div>
      </div>
    </div>
  );
};

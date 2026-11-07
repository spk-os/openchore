// 共享小组件：状态行（loading/empty/error + 重试）、标签、按章汇总条。
// 所有服务端数组字段在这里统一做空值防御（null → 空数组），组件层不再重复判空。

import React from 'react';
import { MathText } from '../components/MathText';
import { chapterBarSegments, statusLabel } from './labels';
import type { ChapterSummaryRow } from './types';
import s from './workspace.module.css';

export const LoadingRow: React.FC<{ label?: string }> = ({ label = '加载中…' }) => (
  <div className={s.loading} data-test="tx-loading" role="status">{label}</div>
);

export const EmptyRow: React.FC<{ children: React.ReactNode; test?: string }> = ({ children, test = 'tx-empty' }) => (
  <div className={s.empty} data-test={test}>{children}</div>
);

export interface ErrorRowProps {
  error: unknown;
  onRetry?: () => void;
  test?: string;
}

/** 错误行：TaxonomyAPIError 取服务端中文消息，其他错误取字符串；带重试按钮。 */
export const ErrorRow: React.FC<ErrorRowProps> = ({ error, onRetry, test = 'tx-error' }) => {
  const msg = error instanceof Error ? error.message : String(error ?? '未知错误');
  return (
    <div className={s.error} data-test={test} role="alert">
      <span>{msg}</span>
      {onRetry && (
        <button type="button" className={s.retry} data-test="tx-retry" onClick={onRetry}>
          重试
        </button>
      )}
    </div>
  );
};

export interface StatusTagProps {
  status: string;
}

const STATUS_CLS: Record<string, string> = {
  weak: s.tagWeak,
  shaky: s.tagShaky,
  mastered: s.tagMastered,
  unknown: s.tagUnknown,
};

/** 四态标签。 */
export const StatusTag: React.FC<StatusTagProps> = ({ status }) => (
  <span className={`${s.tag} ${STATUS_CLS[status] ?? s.tagUnknown}`} data-test="status-tag">
    {statusLabel(status)}
  </span>
);

export interface ChapterBarProps {
  row: ChapterSummaryRow;
}

/** 按章汇总色条：掌握绿 / 不稳橙 / 薄弱红 / 未测灰。 */
export const ChapterBar: React.FC<ChapterBarProps> = ({ row }) => {
  const segs = chapterBarSegments(row);
  const clsMap = { m: s.segM, s: s.segS, w: s.segW, u: s.segU } as const;
  return (
    <span className={s.chapBar} aria-hidden data-test="chapter-bar">
      {segs.map((seg, i) => (
        <i key={i} className={clsMap[seg.cls]} style={{ width: `${seg.pct}%` }} />
      ))}
    </span>
  );
};

export interface MathPromptProps {
  text: string | null | undefined;
  className?: string;
}

/** 题面/答案/解析的 LaTeX 渲染（$…$ / $$…$$），空值渲染 null。 */
export const MathPrompt: React.FC<MathPromptProps> = ({ text, className }) => {
  if (!text) return null;
  return <MathText as="p" className={`${s.promptText} ${className ?? ''}`} text={text} />;
};

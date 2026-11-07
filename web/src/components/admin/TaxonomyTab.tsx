// The parent's taxonomy tab (042): the ten-subject knowledge workspace for
// the selected kid — browse, diagnose, blindspots and history in the shared
// TaxonomyWorkspace (parent tone: 「诊断」/「盲区」, plus the generate-course
// hook), plus the learning-data JSON import (restore the selected kid's
// exported learning data via taxonomyApi.adminImportLearning with
// size/format pre-checks).
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { User } from '../../types';
import { TaxonomyWorkspace } from '../../taxonomy/TaxonomyWorkspace';
import { KnowledgeGraphPanel } from '../../taxonomy/KnowledgeGraphPanel';
import { TaxonomyPrerequisitesCard } from './TaxonomyPrerequisitesCard';
import { TaxonomySharesCard } from './TaxonomySharesCard';
import { taxonomyApi } from '../../taxonomy/client';
import type { ImportLearningResult } from '../../taxonomy/types';
import ui from './ui.module.css';

/** 前端预校验的上限：导出文件（学习数据）实际远小于此，超出即拒绝读取。 */
const IMPORT_MAX_BYTES = 20 * 1024 * 1024;

/** The JSON import card (restore the selected kid's exported learning data).
 *  No target kid → the restore button stays disabled (the restore target is
 *  explicit, never the export document's own user). */
const ImportCard: React.FC<{ targetUserId: number | null }> = ({ targetUserId }) => {
  const { t } = useTranslation();
  const fileRef = useRef<HTMLInputElement | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const pick = async () => {
    setMsg(null);
    const file = fileRef.current?.files?.[0];
    if (!file) return;
    // 没选目标孩子禁止恢复：恢复目标是家长选中的 kid，不是文档里导出时的 user。
    if (targetUserId == null) {
      setMsg({ kind: 'err', text: t('admin.taxonomyTab.importNoTarget') });
      return;
    }
    // Size pre-check before reading anything into memory.
    if (file.size > IMPORT_MAX_BYTES) {
      setMsg({ kind: 'err', text: t('admin.taxonomyTab.importTooLarge', { mb: IMPORT_MAX_BYTES / (1024 * 1024) }) });
      return;
    }
    let payload: unknown;
    try {
      payload = JSON.parse(await file.text());
    } catch {
      setMsg({ kind: 'err', text: t('admin.taxonomyTab.importBadJson') });
      return;
    }
    // Shape pre-check: the export document always names its user (the
    // source user stays in the document for audit; the restore target is
    // targetUserId, sent as target_user_id by the client).
    const userId = (payload as { user?: { id?: unknown } } | null)?.user?.id;
    if (typeof userId !== 'number' || !(userId > 0)) {
      setMsg({ kind: 'err', text: t('admin.taxonomyTab.importNoUser') });
      return;
    }
    setBusy(true);
    try {
      // 学习数据恢复统一端点（POST /api/admin/taxonomy/import；恢复目标是
      // 选中的孩子，发送时非变更地在文档上加 target_user_id）。
      const res: ImportLearningResult = await taxonomyApi.adminImportLearning(payload, targetUserId);
      setMsg({ kind: 'ok', text: t('admin.taxonomyTab.importDone', { imported: res.imported, skipped: res.skipped }) });
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
    setBusy(false);
  };

  return (
    <section className={ui.card} data-test="tx-import-card">
      <div className={ui.sectionHead}>
        <h3 className={ui.sectionTitle}>{t('admin.taxonomyTab.importTitle')}</h3>
        <span className={ui.sectionDesc}>{t('admin.taxonomyTab.importDesc')}</span>
      </div>
      <div className={ui.formRow} data-test="tx-import-form">
        <input
          ref={fileRef}
          type="file"
          accept="application/json,.json"
          aria-label={t('admin.taxonomyTab.importTitle')}
          data-test="tx-import-file"
          disabled={busy}
        />
        <button
          type="button"
          className={ui.btnPrimary}
          data-test="tx-import-btn"
          disabled={busy || targetUserId == null}
          title={targetUserId == null ? t('admin.taxonomyTab.importNoTarget') : undefined}
          onClick={() => { void pick(); }}
        >
          {busy ? t('admin.taxonomyTab.importing') : t('admin.taxonomyTab.importBtn')}
        </button>
      </div>
      {msg && (
        <p
          className={msg.kind === 'ok' ? ui.msgWaiting : ui.msgError}
          role="status"
          data-test={msg.kind === 'ok' ? 'tx-import-ok' : 'tx-import-error'}
        >
          {msg.text}
        </p>
      )}
    </section>
  );
};

/** The workspace header: one kid at a time (the parent picks whose data). */
const KidPicker: React.FC<{
  kids: User[];
  kidId: number | null;
  onChange: (id: number | null) => void;
}> = ({ kids, kidId, onChange }) => {
  const { t } = useTranslation();
  return (
    <label className={ui.field}>
      <span className={ui.label}>{t('admin.taxonomyTab.pickKid')}</span>
      <select
        className={ui.input}
        value={kidId ?? ''}
        onChange={e => onChange(Number(e.target.value) || null)}
        aria-label={t('admin.taxonomyTab.pickKid')}
        data-test="tx-kid-select"
      >
        {kids.length === 0 && <option value="">{t('admin.taxonomyTab.noKids')}</option>}
        {kids.map(k => <option key={k.id} value={k.id}>{k.name}</option>)}
      </select>
    </label>
  );
};

export const TaxonomyTab: React.FC = () => {
  const { t } = useTranslation();
  const [users, setUsers] = useState<User[]>([]);
  const [kidId, setKidId] = useState<number | null>(null);

  useEffect(() => {
    let alive = true;
    api.users.list().then(list => {
      if (!alive) return;
      setUsers(list);
      const first = list.find(u => u.role === 'child');
      if (first) setKidId(first.id);
    }).catch(() => { if (alive) setUsers([]); });
    return () => { alive = false; };
  }, []);

  const kids = useMemo(() => users.filter(u => u.role === 'child'), [users]);

  return (
    <div className={ui.page} data-test="admin-taxonomy-page">
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>🧭 {t('admin.taxonomyTab.heading')}</h2>
          <p className={ui.pageSub}>{t('admin.taxonomyTab.subtitle')}</p>
        </div>
        <KidPicker kids={kids} kidId={kidId} onChange={setKidId} />
      </div>

      <TaxonomyWorkspace audience="parent" userId={kidId} />
      <div style={{ height: 20 }} />
      {/* 真实邻域图谱（卡 TX-GRAPH-PARENT-UI）：放在工作区之后、
          ImportCard 之前；admin 端点要求显式 user_id，kidId 为空时
          面板内部禁用请求并提示先选孩子。 */}
      <KnowledgeGraphPanel userId={kidId} />
      <div style={{ height: 20 }} />
      {/* 先修边管理（卡 TX-PREREQ-UI-CODER，全局内容不 per-kid）与
          分享列表/撤销卡（TX-SHARES-UI-CODER）并列，位于图谱与导入之间。 */}
      <TaxonomyPrerequisitesCard />
      <div style={{ height: 20 }} />
      <TaxonomySharesCard kidId={kidId} />
      <div style={{ height: 20 }} />
      <ImportCard targetUserId={kidId} />
    </div>
  );
};

import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api, APIError } from '../../api';
import type { AgentConnection, AgentConnectionUpdate } from '../../types';
import { Icon } from '../../design';
import { FormMessage, type Msg } from './FormMessage';
import ui from './ui.module.css';

interface Draft {
  baseUrl: string;
  model: string;
  agentRef: string;
  token: string; // blank keeps the saved token
  clearToken: boolean;
}

const toDraft = (c: AgentConnection): Draft => ({
  baseUrl: c.base_url,
  model: c.model,
  agentRef: c.agent_ref || 'spk-tester',
  token: '',
  clearToken: false,
});

const toUpdate = (d: Draft): AgentConnectionUpdate => {
  const u: AgentConnectionUpdate = {
    base_url: d.baseUrl.trim(),
    model: d.model.trim(),
    agent_ref: d.agentRef.trim(),
  };
  if (d.token.trim()) u.token = d.token.trim();
  else if (d.clearToken) u.token = '';
  return u;
};

/** 任务③：spk-agent 接入（/spk-sdk-agent）。文本 AI 任务（学习规划、周报
 * 总结、措施蒸馏/加强）优先派发给 spk-agent（真 agent，带工具），失败或
 * 未配置回落直连 AI 连接；拍照审查/试卷判读保持直连多模态客户端。这里
 * 配 spk-agent 的地址、agent 名、模型与鉴权 token。 */
export const AgentSection: React.FC = () => {
  const { t } = useTranslation();
  const [saved, setSaved] = useState<AgentConnection | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [message, setMessage] = useState<Msg>(null);

  useEffect(() => {
    api.admin.aiConfig().then(cfg => {
      if (!cfg.agent) return;
      setSaved(cfg.agent);
      setDraft(toDraft(cfg.agent));
    }).catch(() => {});
  }, []);

  if (!saved || !draft) return null;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setMessage(null);
    try {
      const cfg = await api.admin.updateAIConfig({ agent: toUpdate(draft) });
      if (cfg.agent) {
        setSaved(cfg.agent);
        setDraft(toDraft(cfg.agent));
      }
      setMessage({ type: 'success', text: t('admin.settingsTab.agent.saveSuccess') });
    } catch (err) {
      const detail = err instanceof APIError ? err.message : '';
      setMessage({ type: 'error', text: t('admin.settingsTab.agent.saveError', { detail }) });
    }
    setSaving(false);
  };

  // A quick live check: the configured spk-agent answers /health.
  const handleTest = async () => {
    if (!draft.baseUrl.trim()) return;
    setTesting(true);
    setMessage(null);
    try {
      const res = await fetch(draft.baseUrl.trim().replace(/\/+$/, '') + '/health');
      setMessage(res.ok
        ? { type: 'success', text: t('admin.settingsTab.agent.testSuccess') }
        : { type: 'error', text: t('admin.settingsTab.agent.testStatusError', { status: res.status }) });
    } catch {
      setMessage({ type: 'error', text: t('admin.settingsTab.agent.testNetworkError') });
    }
    setTesting(false);
  };

  if (saved.from_env) {
    return (
      <section className={clsx(ui.card, ui.section)} aria-labelledby="settings-agent">
        <h3 className={ui.sectionTitle} id="settings-agent"><Icon name="spark" /> {t('admin.settingsTab.agent.title')}</h3>
        <p className={ui.sectionDesc}>{t('admin.settingsTab.agent.description')}</p>
        <p className={ui.help}>{t('admin.settingsTab.agent.fromEnv')}</p>
        <p className={ui.rowMeta}><code className={ui.code}>{saved.base_url}</code></p>
      </section>
    );
  }

  return (
    <form className={clsx(ui.card, ui.section)} onSubmit={handleSubmit} aria-labelledby="settings-agent">
      <h3 className={ui.sectionTitle} id="settings-agent"><Icon name="spark" /> {t('admin.settingsTab.agent.title')}</h3>
      <p className={ui.sectionDesc}>{t('admin.settingsTab.agent.description')}</p>
      <span className={ui.help}>{t('admin.settingsTab.agent.help')}</span>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.settingsTab.agent.baseUrlLabel')}</span>
        <input
          className={ui.input}
          type="url"
          value={draft.baseUrl}
          onChange={e => setDraft({ ...draft, baseUrl: e.target.value })}
          placeholder={t('admin.settingsTab.agent.baseUrlPlaceholder')}
        />
      </label>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.settingsTab.agent.agentRefLabel')}</span>
        <input
          className={ui.input}
          value={draft.agentRef}
          onChange={e => setDraft({ ...draft, agentRef: e.target.value })}
          placeholder="spk-tester"
        />
        <span className={ui.help}>{t('admin.settingsTab.agent.agentRefHelp')}</span>
      </label>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.settingsTab.agent.modelLabel')}</span>
        <input
          className={ui.input}
          value={draft.model}
          onChange={e => setDraft({ ...draft, model: e.target.value })}
          placeholder={t('admin.settingsTab.agent.modelPlaceholder')}
        />
        <span className={ui.help}>{t('admin.settingsTab.agent.modelHelp')}</span>
      </label>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.settingsTab.agent.tokenLabel')}</span>
        <input
          className={ui.input}
          type="password"
          autoComplete="off"
          value={draft.token}
          onChange={e => setDraft({ ...draft, token: e.target.value, clearToken: false })}
          placeholder={saved.token_set
            ? t('admin.settingsTab.agent.tokenSavedPlaceholder')
            : t('admin.settingsTab.agent.tokenPlaceholder')}
        />
        <span className={ui.help}>{t('admin.settingsTab.agent.tokenHelp')}</span>
      </label>
      {saved.token_set && !draft.token && (
        <label className={ui.check}>
          <input type="checkbox" checked={draft.clearToken} onChange={e => setDraft({ ...draft, clearToken: e.target.checked })} />
          <span className={ui.checkLabel}>{t('admin.settingsTab.agent.clearToken')}</span>
        </label>
      )}
      <FormMessage msg={message} />
      <div className={ui.actionsEnd}>
        {draft.baseUrl.trim() && (
          <button type="button" className={ui.btnGhost} disabled={testing} onClick={handleTest}>
            {testing ? t('admin.settingsTab.agent.testingButton') : t('admin.settingsTab.agent.testButton')}
          </button>
        )}
        <button type="submit" className={ui.btnPrimary} disabled={saving}>
          <Icon name="check" /> {saving ? t('admin.settingsTab.agent.savingButton') : t('admin.settingsTab.agent.saveButton')}
        </button>
      </div>
    </form>
  );
};

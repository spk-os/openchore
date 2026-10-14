// The parent's learner-profile tab (L3, OD parent-profile.html): the
// hero strip (four stats), the KC mastery matrix (three mastery colors,
// low-confidence greyed), the 14-day trend, the Ranedeer-style
// student_config editor, and the cited AI plan feed (every suggestion
// carries its source refs — DeepTutor's auditability; adopting goes
// through the copilot apply path, the parent always has the final say).
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type {
  KCMasteryStat, LearnerProfile, PlannerChore, PlannerRef, StudentConfig, User,
} from '../../types';
import ui from './ui.module.css';
import styles from './ProfileTab.module.css';

/** The three depth segments of the OD pcfg-depth radiogroup. */
const DEPTHS: StudentConfig['depth'][] = ['小学', '初中', '高中'];

/** The OD pcfg-style chips (Ranedeer learning_style enum → 中文标签). */
const STYLE_OPTIONS: { value: StudentConfig['learning_style']; label: string }[] = [
  { value: 'Visual', label: '视觉型' },
  { value: 'Verbal', label: '语言型' },
  { value: 'Active', label: '主动型' },
  { value: 'Intuitive', label: '直觉型' },
  { value: 'Reflective', label: '反思型' },
  { value: 'Global', label: '全局型' },
];

/** The OD pcfg-comm chips (Ranedeer communication enum → 中文标签). */
const COMM_OPTIONS: { value: StudentConfig['communication']; label: string }[] = [
  { value: 'Formal', label: '正式' },
  { value: 'Textbook', label: '教科书式' },
  { value: 'Layman', label: '大白话' },
  { value: 'StoryTelling', label: '讲故事' },
  { value: 'Socratic', label: '苏格拉底式' },
];

/** The OD pcfg-tone chips (Ranedeer tone enum → 中文标签). */
const TONE_OPTIONS: { value: StudentConfig['tone']; label: string }[] = [
  { value: 'Encouraging', label: '鼓励为主' },
  { value: 'Neutral', label: '中性' },
  { value: 'Informative', label: '信息型' },
  { value: 'Friendly', label: '友好' },
  { value: 'Humorous', label: '幽默' },
];

/** Parse a JSON string array ("" / invalid → []). */
function parseList(s: string): string[] {
  try {
    const v = JSON.parse(s);
    return Array.isArray(v) ? v.map(String) : [];
  } catch {
    return [];
  }
}

/** The mastery bucket of one KC row: OD's <50% red / 50-79% amber / ≥80% green. */
function masteryBucket(m: number): 'weak' | 'mid' | 'good' {
  if (m < 0.5) return 'weak';
  if (m < 0.8) return 'mid';
  return 'good';
}

/** One KC row of the matrix (the OD pp-kc-discriminant shape). */
const KCRow: React.FC<{ stat: KCMasteryStat }> = ({ stat }) => {
  const { t } = useTranslation();
  const bucket = masteryBucket(stat.mastery);
  const pct = Math.round(stat.mastery * 100);
  return (
    <div
      className={`${styles.kc} ${stat.low_confidence ? styles.kcLow : ''}`}
      data-test="pp-kc-row"
      data-mastery={bucket}
    >
      <span className={styles.kcName}>
        {stat.name}
        {bucket === 'weak' && <span className={styles.pillWeak}>⚠ {t('admin.profileTab.kcWeak')}</span>}
        {bucket === 'good' && !stat.low_confidence && <span className={styles.pillGood}>{t('admin.profileTab.kcStrength')}</span>}
      </span>
      <span className={styles.kcBarWrap}>
        <b className={styles.kcPct} style={{ color: `var(--${bucket === 'weak' ? 'urgent' : bucket === 'mid' ? 'star' : 'done'})` }}>{pct}%</b>
        <span className={styles.kcBar} role="img" aria-label={t('admin.profileTab.kcAria', { name: stat.name, pct })}>
          <i
            className={`${styles.kcBarFill} ${bucket === 'weak' ? styles.fillWeak : bucket === 'mid' ? styles.fillMid : styles.fillGood}`}
            style={{ width: `${pct}%` }}
          />
        </span>
      </span>
      <span className={styles.kcMeta}>
        n={stat.total_count}
        {stat.low_confidence ? ` · ${t('admin.profileTab.kcLowConfidence')}` : ''}
      </span>
    </div>
  );
};

export const ProfileTab: React.FC = () => {
  const { t } = useTranslation();

  const [users, setUsers] = useState<User[]>([]);
  const [kidId, setKidId] = useState<number | null>(null);
  const [profile, setProfile] = useState<LearnerProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);

  // The config editor's working copy (interests as a live string list).
  const [cfg, setCfg] = useState<StudentConfig | null>(null);
  const [interests, setInterests] = useState<string[]>([]);
  const [interestDraft, setInterestDraft] = useState('');
  const [savingCfg, setSavingCfg] = useState(false);
  const [cfgSaved, setCfgSaved] = useState(false);

  // The plan feed: the planner's answer + adopt/ignore bookkeeping.
  const [planRequest, setPlanRequest] = useState('');
  const [planning, setPlanning] = useState(false);
  const [planError, setPlanError] = useState('');
  const [planChores, setPlanChores] = useState<(PlannerChore & { adopted?: boolean; ignored?: boolean })[]>([]);
  const [planRefs, setPlanRefs] = useState<PlannerRef[]>([]);
  const [planNotes, setPlanNotes] = useState('');
  const [applyingPlan, setApplyingPlan] = useState(false);

  const kids = useMemo(() => users.filter(u => u.role === 'child'), [users]);

  // Load people, then default to the first kid.
  useEffect(() => {
    api.users.list().then(list => {
      setUsers(list);
      const first = list.find(u => u.role === 'child');
      if (first) setKidId(first.id);
    }).catch(() => setFailed(true));
  }, []);

  const loadProfile = useCallback(async (id: number) => {
    setLoading(true);
    setFailed(false);
    try {
      const p = await api.admin.learnerProfile(id);
      setProfile(p);
      setCfg(p.config);
      setInterests(parseList(p.config.interests));
      setCfgSaved(false);
    } catch {
      setFailed(true);
      setProfile(null);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    if (kidId != null) {
      loadProfile(kidId);
      // Reset the plan feed when switching kids.
      setPlanChores([]);
      setPlanRefs([]);
      setPlanNotes('');
      setPlanError('');
    }
  }, [kidId, loadProfile]);

  // --- config editing ---

  const saveConfig = async () => {
    if (kidId == null || !cfg) return;
    setSavingCfg(true);
    setCfgSaved(false);
    try {
      const saved = await api.admin.updateStudentConfig(kidId, {
        ...cfg,
        interests: JSON.stringify(interests),
      });
      setCfg(saved);
      setCfgSaved(true);
    } catch {
      setFailed(true);
    }
    setSavingCfg(false);
  };

  const removeInterest = (idx: number) => {
    setInterests(prev => prev.filter((_, i) => i !== idx));
  };

  const addInterest = () => {
    const v = interestDraft.trim();
    if (!v || interests.includes(v)) return;
    setInterests(prev => [...prev, v]);
    setInterestDraft('');
  };

  // --- plan feed ---

  const runPlanner = async () => {
    if (kidId == null) return;
    setPlanning(true);
    setPlanError('');
    try {
      const resp = await api.admin.copilotPlan({
        assigned_to: kidId,
        request: planRequest.trim() || undefined,
      });
      setPlanChores(resp.plan.proposal.new_chores.map(c => ({ ...c })));
      setPlanRefs(resp.plan.source_refs);
      setPlanNotes(resp.plan.proposal.notes_to_parent);
    } catch (err) {
      setPlanError(err instanceof Error && err.message ? err.message : t('admin.profileTab.planFailed'));
      setPlanChores([]);
      setPlanRefs([]);
    }
    setPlanning(false);
  };

  // Adopt one proposed chore: through the copilot apply path — the same
  // store path a manual create uses (AI 只建议不决定).
  const adoptChore = async (idx: number) => {
    if (kidId == null) return;
    const c = planChores[idx];
    if (!c || c.adopted || c.ignored) return;
    setApplyingPlan(true);
    try {
      await api.admin.copilotApply({
        assigned_to: kidId,
        source: 'copilot',
        tasks: [{
          title: c.title,
          description: c.description,
          category: c.category,
          icon: c.icon,
          points_value: c.points_value,
          missed_penalty: c.missed_penalty,
          estimated_minutes: c.estimated_minutes,
          days: c.days,
          date: c.date,
          due_by: c.due_by,
          available_at: c.available_at,
          expiry: 'block' as const,
        }],
      });
      setPlanChores(prev => prev.map((x, i) => (i === idx ? { ...x, adopted: true } : x)));
    } catch {
      setPlanError(t('admin.profileTab.adoptFailed'));
    }
    setApplyingPlan(false);
  };

  const ignoreChore = (idx: number) => {
    setPlanChores(prev => prev.map((x, i) => (i === idx ? { ...x, ignored: true } : x)));
  };

  // The KC matrix grouped by subject (the OD pp-kc-matrix groups).
  const kcBySubject = useMemo(() => {
    const groups = new Map<string, KCMasteryStat[]>();
    for (const s of profile?.kc_mastery ?? []) {
      const list = groups.get(s.subject) ?? [];
      list.push(s);
      groups.set(s.subject, list);
    }
    return [...groups.entries()];
  }, [profile]);

  const trendMax = useMemo(() => {
    let max = 0;
    for (const d of profile?.event_trend ?? []) max = Math.max(max, d.events);
    return Math.max(max, 1);
  }, [profile]);

  const hero = profile?.hero;

  return (
    <div className={styles.ppPage} data-test="profile-tab">
      <div className={ui.pageHead}>
        <div>
          <h1 className={ui.pageTitle}>{t('admin.profileTab.heading')}</h1>
          <p className={ui.pageSub}>{t('admin.profileTab.subtitle')}</p>
        </div>
        <select
          className={styles.kidPicker}
          value={kidId ?? ''}
          aria-label={t('admin.profileTab.pickKid')}
          onChange={e => setKidId(Number(e.target.value) || null)}
        >
          {kids.length === 0 && <option value="">{t('admin.profileTab.noKids')}</option>}
          {kids.map(k => <option key={k.id} value={k.id}>{k.name}</option>)}
        </select>
      </div>

      {loading && <p className={ui.pageSub}>{t('admin.profileTab.loading')}</p>}
      {failed && <p className={ui.pageSub} role="alert">{t('admin.profileTab.loadFailed')}</p>}

      {profile && hero && cfg && !loading && (
        <>
          {/* hero: the OD pp-hero strip */}
          <section className={styles.ppHero} data-test="pp-hero">
            <div>
              <p className={styles.ppHeroName}>{profile.kid_name}</p>
              <div className={styles.ppHeroStats}>
                <span className={styles.ppStat} data-test="pp-stat-tasks">
                  <b className={styles.ppStatValue}>{hero.chores_this_week}</b>
                  <span className={styles.ppStatLabel}>{t('admin.profileTab.statTasks')}</span>
                </span>
                <span className={styles.ppStat} data-test="pp-stat-fixrate">
                  <b className={styles.ppStatValueDone}>{Math.round(hero.fix_rate * 100)}%</b>
                  <span className={styles.ppStatLabel}>{t('admin.profileTab.statFixRate')}</span>
                </span>
                <span className={styles.ppStat} data-test="pp-stat-review">
                  <b className={styles.ppStatValue}>🔥 {hero.review_streak}</b>
                  <span className={styles.ppStatLabel}>{t('admin.profileTab.statReview')}</span>
                </span>
                <span className={styles.ppStat} data-test="pp-stat-kc">
                  <b className={styles.ppStatValueUrgent}>{hero.weak_kc_count}</b>
                  <span className={styles.ppStatLabel}>{t('admin.profileTab.statWeakKc')}</span>
                </span>
              </div>
            </div>
          </section>

          {/* KC matrix: grouped by subject, weakest first (server order). */}
          <section className={ui.card} data-test="pp-kc-matrix">
            <div className={ui.sectionHead}>
              <h2 className={ui.sectionTitle}>{t('admin.profileTab.kcMatrixTitle')}</h2>
              <span className={ui.sectionDesc}>{t('admin.profileTab.kcMatrixDesc')}</span>
            </div>
            {profile.kc_mastery.length === 0 && (
              <p className={ui.emptyInline}>{t('admin.profileTab.kcEmpty')}</p>
            )}
            <div className={styles.mxGroup}>
              {kcBySubject.map(([subject, rows]) => (
                <div key={subject} className={styles.mxGroup}>
                  <div className={styles.mxGroupHead}>
                    <span>{t(`admin.analysis.subject_${subject}`) !== `admin.analysis.subject_${subject}` ? t(`admin.analysis.subject_${subject}`) : subject}</span>
                    <span className={styles.mxGroupCount}>{t('admin.profileTab.kcSampleCount', { count: rows.reduce((n, r) => n + r.total_count, 0) })}</span>
                  </div>
                  {rows.map(r => <KCRow key={r.tag_id} stat={r} />)}
                </div>
              ))}
            </div>
          </section>

          {/* 14-day trend bars. */}
          <section className={ui.card} data-test="pp-trend">
            <div className={ui.sectionHead}>
              <h2 className={ui.sectionTitle}>{t('admin.profileTab.trendTitle')}</h2>
              <span className={ui.sectionDesc}>{t('admin.profileTab.trendDesc')}</span>
            </div>
            <div className={styles.trend} data-test="pp-trend-chart" role="img" aria-label={t('admin.profileTab.trendAria')}>
              {profile.event_trend.map(d => (
                <span key={d.day} className={styles.trendCol} title={`${d.day}: ${d.events}`}>
                  <i className={styles.trendBar} style={{ height: `${Math.max((d.events / trendMax) * 100, 4)}%` }} />
                  {d.events > 0 && <span className={styles.trendValue}>{d.events}</span>}
                </span>
              ))}
            </div>
            <div className={styles.trend}>
              {profile.event_trend.map(d => (
                <span key={d.day} className={styles.trendLabel}>{d.day.slice(5)}</span>
              ))}
            </div>
          </section>

          {/* student_config editor: the OD pp-config card. */}
          <section className={ui.card} data-test="pp-config">
            <div className={ui.sectionHead}>
              <h2 className={ui.sectionTitle}>{t('admin.profileTab.cfgTitle')}</h2>
              <span className={ui.badge}>✨ {t('admin.profileTab.cfgBadge')}</span>
            </div>

            <div className={styles.cfgRow} data-test="pcfg-depth">
              <span className={styles.cfgLabel}>{t('admin.profileTab.cfgDepth')}</span>
              <div className={ui.chips} role="radiogroup" aria-label={t('admin.profileTab.cfgDepth')}>
                {DEPTHS.map(d => (
                  <button
                    key={d}
                    type="button"
                    className={cfg.depth === d ? ui.chipOn : ui.chip}
                    aria-pressed={cfg.depth === d}
                    onClick={() => setCfg({ ...cfg, depth: d })}
                  >
                    {t(`admin.profileTab.depth_${d}`)}
                  </button>
                ))}
              </div>
              <p className={styles.cfgHint}>{t('admin.profileTab.cfgDepthHint')}</p>
            </div>

            <div className={styles.cfgRow} data-test="pcfg-style">
              <span className={styles.cfgLabel}>{t('admin.profileTab.cfgStyle')}</span>
              <div className={ui.chips} role="group" aria-label={t('admin.profileTab.cfgStyle')}>
                {STYLE_OPTIONS.map(o => (
                  <button
                    key={o.value}
                    type="button"
                    className={cfg.learning_style === o.value ? ui.chipOn : ui.chip}
                    aria-pressed={cfg.learning_style === o.value}
                    onClick={() => setCfg({ ...cfg, learning_style: o.value })}
                  >
                    {o.label}
                  </button>
                ))}
              </div>
              <p className={styles.cfgHint}>{t('admin.profileTab.cfgStyleHint')}</p>
            </div>

            <div className={styles.cfgRow} data-test="pcfg-comm">
              <span className={styles.cfgLabel}>{t('admin.profileTab.cfgComm')}</span>
              <div className={ui.chips} role="group" aria-label={t('admin.profileTab.cfgComm')}>
                {COMM_OPTIONS.map(o => (
                  <button
                    key={o.value}
                    type="button"
                    className={cfg.communication === o.value ? ui.chipOn : ui.chip}
                    aria-pressed={cfg.communication === o.value}
                    onClick={() => setCfg({ ...cfg, communication: o.value })}
                  >
                    {o.label}
                  </button>
                ))}
              </div>
              <p className={styles.cfgHint}>{t('admin.profileTab.cfgCommHint')}</p>
            </div>

            <div className={styles.cfgRow} data-test="pcfg-tone">
              <span className={styles.cfgLabel}>{t('admin.profileTab.cfgTone')}</span>
              <div className={ui.chips} role="group" aria-label={t('admin.profileTab.cfgTone')}>
                {TONE_OPTIONS.map(o => (
                  <button
                    key={o.value}
                    type="button"
                    className={cfg.tone === o.value ? ui.chipOn : ui.chip}
                    aria-pressed={cfg.tone === o.value}
                    onClick={() => setCfg({ ...cfg, tone: o.value })}
                  >
                    {o.label}
                  </button>
                ))}
              </div>
              <p className={styles.cfgHint}>{t('admin.profileTab.cfgToneHint')}</p>
            </div>

            <div className={styles.cfgRow} data-test="pcfg-interests">
              <span className={styles.cfgLabel}>{t('admin.profileTab.cfgInterests')}</span>
              <div className={styles.tagList}>
                {interests.map((tag, idx) => (
                  <span key={tag} className={styles.tagItem}>
                    {tag}
                    <button
                      type="button"
                      aria-label={t('admin.profileTab.removeInterest', { tag })}
                      onClick={() => removeInterest(idx)}
                    >✕</button>
                  </span>
                ))}
                <input
                  className={styles.tagAddInput}
                  value={interestDraft}
                  placeholder={t('admin.profileTab.addInterestPlaceholder')}
                  aria-label={t('admin.profileTab.addInterestPlaceholder')}
                  onChange={e => setInterestDraft(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addInterest(); } }}
                />
                <button type="button" className={ui.btnGhost} onClick={addInterest}>
                  {t('admin.profileTab.addInterest')}
                </button>
              </div>
              <p className={styles.cfgHint}>{t('admin.profileTab.cfgInterestsHint')}</p>
            </div>

            <div className={styles.saveRow}>
              <button
                type="button"
                className={ui.btnPrimary}
                data-test="pp-config-save"
                disabled={savingCfg}
                onClick={saveConfig}
              >
                {savingCfg ? t('admin.profileTab.saving') : t('admin.profileTab.saveCfg')}
              </button>
              {cfgSaved && <span className={styles.saveNote}>{t('admin.profileTab.cfgSaved')}</span>}
              <span className={styles.saveNote}>{t('admin.profileTab.cfgSaveNote')}</span>
            </div>
          </section>

          {/* The cited plan feed. */}
          <section className={ui.card} data-test="pp-plan-feed">
            <div className={ui.sectionHead}>
              <h2 className={ui.sectionTitle}>{t('admin.profileTab.planTitle')}</h2>
              <span className={ui.sectionDesc}>{t('admin.profileTab.planDesc')}</span>
            </div>

            <div className={styles.planRequestRow}>
              <input
                className={styles.planRequestInput}
                value={planRequest}
                placeholder={t('admin.profileTab.planRequestPlaceholder')}
                aria-label={t('admin.profileTab.planRequestPlaceholder')}
                onChange={e => setPlanRequest(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); void runPlanner(); } }}
              />
              <button type="button" className={ui.btnPrimary} disabled={planning} onClick={runPlanner}>
                {planning ? t('admin.profileTab.planning') : t('admin.profileTab.planBtn')}
              </button>
            </div>

            {planError && <p className={ui.pageSub} role="alert">{planError}</p>}

            {planChores.length === 0 && !planning && !planError && (
              <p className={ui.emptyInline}>{t('admin.profileTab.planEmpty')}</p>
            )}

            {planChores.map((c, idx) => (
              <div key={idx} className={styles.plan} data-test={`pp-plan-${idx + 1}`} data-state={c.adopted ? 'adopted' : c.ignored ? 'ignored' : 'open'}>
                <div className={styles.planHead}>
                  <span className={styles.planIcon} aria-hidden>{c.icon || '✨'}</span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <p className={styles.planTitle}>{c.title}</p>
                    <p className={styles.planDesc}>{c.description}</p>
                  </div>
                  <span className={ui.badge} data-test={`pp-plan-${idx + 1}-prio`}>
                    {c.confidence >= 0.8 ? t('admin.profileTab.prioHigh') : c.confidence >= 0.5 ? t('admin.profileTab.prioMid') : t('admin.profileTab.prioLow')}
                  </span>
                </div>
                {c.rationale && <p className={styles.planDesc}>{c.rationale}</p>}
                <div className={styles.planRefs} data-test={`pp-plan-${idx + 1}-refs`}>
                  {planRefs.map((ref, i) => (
                    <p key={i} className={styles.planRef}>
                      📊 {t('admin.profileTab.refBasedOn')}：{ref.fact}
                      <span className={styles.refSrc}>{ref.type}{ref.id != null ? ` #${ref.id}` : ''}</span>
                    </p>
                  ))}
                </div>
                <div className={styles.planActs}>
                  {c.adopted ? (
                    <span className={ui.badge}>✅ {t('admin.profileTab.adopted')}</span>
                  ) : c.ignored ? (
                    <span className={ui.badgeOutline}>{t('admin.profileTab.ignored')}</span>
                  ) : (
                    <>
                      <button
                        type="button"
                        className={styles.btnPrimarySm}
                        data-test={`pp-plan-${idx + 1}-adopt`}
                        disabled={applyingPlan}
                        onClick={() => adoptChore(idx)}
                      >
                        {t('admin.profileTab.adopt')}
                      </button>
                      <button
                        type="button"
                        className={styles.btnSm}
                        data-test={`pp-plan-${idx + 1}-ignore`}
                        disabled={applyingPlan}
                        onClick={() => ignoreChore(idx)}
                      >
                        {t('admin.profileTab.ignore')}
                      </button>
                    </>
                  )}
                  <span className={styles.planActsNote}>{t('admin.profileTab.ignoreNote')}</span>
                </div>
              </div>
            ))}

            {planNotes && <p className={styles.planDesc}>💡 {planNotes}</p>}

            <p className={styles.planNote} data-test="pp-plan-note">{t('admin.profileTab.planNote')}</p>
          </section>

          {/* The rail cards: confidence/method, how the profile is used. */}
          <div className={styles.ppRail}>
            <section className={styles.railCard} data-test="pp-conf-card">
              <h3 className={styles.railCardTitle}>{t('admin.profileTab.confCardTitle')}</h3>
              <p>{t('admin.profileTab.confCardLow')}</p>
              <p>{t('admin.profileTab.confCardSources')}</p>
            </section>
            <section className={styles.railCard} data-test="pp-links-card">
              <h3 className={styles.railCardTitle}>{t('admin.profileTab.linksCardTitle')}</h3>
              <p>{t('admin.profileTab.linksCopilot')}</p>
              <p>{t('admin.profileTab.linksErrorbook')}</p>
              <p>{t('admin.profileTab.linksCourses')}</p>
            </section>
          </div>
        </>
      )}
    </div>
  );
};

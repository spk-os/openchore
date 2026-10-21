import React, { useState, useEffect, useCallback, useSyncExternalStore } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { Settings, BarChart3 } from 'lucide-react';
import { api } from '../api';
import { useAuth } from '../AuthContext';
import { HouseScope, Icon, type IconName } from '../design';
import styles from './AdminDashboard.module.css';
import QuickAssign from '../components/QuickAssign/QuickAssign';
import { ChoresTab } from '../components/admin/ChoresTab';
import { ApprovalsTab } from '../components/admin/ApprovalsTab';
import { UsersTab } from '../components/admin/UsersTab';
import { RewardsTab } from '../components/admin/RewardsTab';
import { PointsTab } from '../components/admin/PointsTab';
import { ActivityTab } from '../components/admin/ActivityTab';
import { SettingsTab } from '../components/admin/SettingsTab';
import { KidsStatusTab } from '../components/admin/KidsStatusTab';
import { TemplatesTab } from '../components/admin/TemplatesTab';
import { CoursesTab } from '../components/admin/CoursesTab';
import { ProfileTab } from '../components/admin/ProfileTab';
import { ErrorbookTab } from '../components/admin/ErrorbookTab';
import { MeasuresTab } from '../components/admin/MeasuresTab';
import { MediaTab } from '../components/admin/MediaTab';
import { LanguageSelector } from '../components/LanguageSelector/LanguageSelector';

type Tab = 'kids-status' | 'approvals' | 'media' | 'chores' | 'templates' | 'courses' | 'profile' | 'errorbook' | 'measures' | 'rewards' | 'points' | 'activity' | 'users' | 'settings';

const TABS: { id: Tab; label: string; icon?: IconName }[] = [
  { id: 'kids-status', label: 'admin.dashboard.tabKids', icon: 'home' },
  { id: 'approvals', label: 'admin.dashboard.tabApprovals', icon: 'check' },
  // 036: the media library — every photo / video / recording in one
  // filtered grid, right where parents already review them.
  { id: 'media', label: 'admin.dashboard.tabMedia', icon: 'camera' },
  { id: 'chores', label: 'admin.dashboard.tabChores', icon: 'broom' },
  { id: 'templates', label: 'admin.dashboard.tabTemplates', icon: 'cal' },
  { id: 'courses', label: 'admin.dashboard.tabCourses', icon: 'book' },
  { id: 'profile', label: 'admin.dashboard.tabProfile', icon: 'spark' },
  // The L1 pair: the questions (error book) and the rules they distilled
  // into (measures) sit together after the learning tabs.
  { id: 'errorbook', label: 'admin.dashboard.tabErrorbook', icon: 'book' },
  { id: 'measures', label: 'admin.dashboard.tabMeasures', icon: 'spark' },
  { id: 'rewards', label: 'admin.dashboard.tabRewards', icon: 'gift' },
  { id: 'points', label: 'admin.dashboard.tabPoints', icon: 'star' },
  { id: 'activity', label: 'admin.dashboard.tabLog', icon: 'clock' },
  { id: 'users', label: 'admin.dashboard.tabPeople', icon: 'people' },
  // Settings stays last (e2e: `nav button` last).
  { id: 'settings', label: 'admin.dashboard.tabSettings' },
];

// 六分组导航（OD spk-openchore admin-*.html 的契约）：桌面 ≥1024px 固定
// 左侧边栏，分组标题 + 成员项；手机顶部横向导航只放高频 4 项 +「更多」
// 展开分组抽屉。nav 顺序保持 TABS 数组顺序（设置最后）。
const NAV_GROUPS: { label: string; tabs: Tab[] }[] = [
  { label: 'admin.dashboard.groupOverview', tabs: ['kids-status'] },
  { label: 'admin.dashboard.groupTodo', tabs: ['approvals', 'media'] },
  { label: 'admin.dashboard.groupTasks', tabs: ['chores', 'templates', 'courses'] },
  { label: 'admin.dashboard.groupLearning', tabs: ['profile', 'errorbook', 'measures'] },
  { label: 'admin.dashboard.groupRewards', tabs: ['rewards', 'points'] },
  { label: 'admin.dashboard.groupFamily', tabs: ['activity', 'users', 'settings'] },
];

// 手机顶部导航的高频 4 项（OD 原型：今天 / 审批 / 媒体库 / 任务）。
const MOBILE_TABS: Tab[] = ['kids-status', 'approvals', 'media', 'chores'];

const TAB_OF = Object.fromEntries(TABS.map(x => [x.id, x])) as unknown as Record<Tab, { label: string; icon?: IconName }>;

/** ≥1024px：桌面侧边栏形态（顶导与抽屉不渲染）。SSR 一律按手机形态。 */
const DESKTOP_QUERY = '(min-width: 1024px)';
const subscribeDesktop = (cb: () => void) => {
  const mq = window.matchMedia(DESKTOP_QUERY);
  mq.addEventListener('change', cb);
  return () => mq.removeEventListener('change', cb);
};
const getDesktop = () => window.matchMedia(DESKTOP_QUERY).matches;
function useDesktopNav(): boolean {
  return useSyncExternalStore(subscribeDesktop, getDesktop, () => false);
}

/** The four overlapping dots of the House logo, one per person colour. */
const LogoMark: React.FC = () => (
  <span className={styles.logo} aria-hidden>
    <i data-person="coral" /><i data-person="mint" /><i data-person="butter" /><i data-person="sky" />
  </span>
);

/** One nav item: the shared button the sidebar, the top nav and the sheet
 *  all render, approvals badge included. */
function NavItem({ id, on, onClick, pendingCount }: { id: Tab; on: boolean; onClick: () => void; pendingCount: number }) {
  const { t } = useTranslation();
  const { label, icon } = TAB_OF[id];
  return (
    <button
      type="button"
      className={clsx(styles.tab, on && styles.tabOn)}
      aria-current={on ? 'page' : undefined}
      onClick={onClick}
    >
      {icon ? <Icon name={icon} /> : <Settings aria-hidden />}
      <span>{t(label)}</span>
      {id === 'approvals' && pendingCount > 0 && (
        <span className={styles.count}>{pendingCount}</span>
      )}
    </button>
  );
}

export const AdminDashboard: React.FC = () => {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const { user, session } = useAuth();
  // The tab is state, not a route — but deep links (#tab=errorbook) and the
  // paper-analysis report's 「接着去哪」 links need to move it from outside
  // the component, so a tiny hash protocol covers both: the hash names the
  // tab, and hashchange lands on it (and clears the hash so refresh stays
  // on the default).
  const tabFromHash = () => {
    const m = window.location.hash.match(/^#tab=([a-z-]+)/);
    return m && (TABS.some(x => x.id === m[1])) ? (m[1] as Tab) : null;
  };
  const [tab, setTab] = useState<Tab>(() => tabFromHash() ?? 'kids-status');
  const [pendingCount, setPendingCount] = useState(0);
  const [quickAssignOpen, setQuickAssignOpen] = useState(false);
  // The QuickAssign tab to open with: 'copilot' when the entry came from
  // the wizard's 「让 AI 建议与规划任务」 (问题③), else the quick tab.
  const [quickAssignMode, setQuickAssignMode] = useState<'quick' | 'copilot'>('quick');
  // 手机「更多」分组抽屉。
  const [moreOpen, setMoreOpen] = useState(false);
  const desktop = useDesktopNav();

  const openQuickAssign = useCallback((mode: 'quick' | 'copilot' = 'quick') => {
    setQuickAssignMode(mode);
    setQuickAssignOpen(true);
  }, []);

  // 分组抽屉的 body 滚动锁（Sheet 同款行为，不打断面板内滚动）。
  useEffect(() => {
    if (!moreOpen) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => { document.body.style.overflow = prev; };
  }, [moreOpen]);

  const goTab = useCallback((id: Tab) => {
    setTab(id);
    setMoreOpen(false);
  }, []);

  useEffect(() => {
    const onHash = () => {
      const next = tabFromHash();
      if (next) {
        goTab(next);
        // Strip the hash so a refresh returns to the default tab and the
        // browser back button doesn't replay tab switches.
        window.history.replaceState(null, '', window.location.pathname);
      }
    };
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, [goTab]);

  // Escape 关抽屉。
  useEffect(() => {
    if (!moreOpen) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setMoreOpen(false); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [moreOpen]);

  const refreshCount = useCallback(() => {
    api.chores.listPending().then(p => setPendingCount(p.length)).catch(() => {});
  }, []);

  // Keep the approvals badge fresh.
  useEffect(() => {
    refreshCount();
    const interval = setInterval(refreshCount, 30000);
    return () => clearInterval(interval);
  }, [refreshCount]);

  const backLabel = t('admin.dashboard.backToMyChores', { name: user?.name ?? '' });

  return (
    // Personal (OIDC) sessions follow the system setting; the shared tablet
    // (tap/PIN) follows the evening schedule.
    <HouseScope mode="auto" persistent={!!session?.persistent} className={styles.shell}>
      {/* 桌面侧边栏（≥1024px）：分组标题 + 全部 14 项，设置在最后。 */}
      <aside className={styles.sidebar} aria-label={t('admin.dashboard.navLabel')}>
        <div className={styles.sidebarHead}>
          <button type="button" className={styles.back} onClick={() => navigate('/')} aria-label={backLabel} title={backLabel}>
            <Icon name="back" />
          </button>
          <div className={styles.brand}>
            <LogoMark />
            <h1 className={styles.title}>{t('admin.dashboard.title')}</h1>
          </div>
        </div>
        <nav className={styles.sidebarNav}>
          {NAV_GROUPS.map(group => (
            <div key={group.label} className={styles.sideGroup}>
              <p className={styles.sideGroupTitle}>{t(group.label)}</p>
              {group.tabs.map(id => (
                <NavItem key={id} id={id} on={tab === id} onClick={() => goTab(id)} pendingCount={pendingCount} />
              ))}
            </div>
          ))}
        </nav>
        <button type="button" className={styles.sideNew} onClick={() => openQuickAssign()}>
          <Icon name="plus" />
          <span>{t('admin.dashboard.quickAssign')}</span>
        </button>
      </aside>

      <div className={styles.mainCol}>
        <header className={styles.header}>
          <div className={styles.headerInner}>
            {/* 手机：返回 + 标题；桌面侧边栏已带标题，这里只留动作。 */}
            <button type="button" className={clsx(styles.back, styles.backMobile)} onClick={() => navigate('/')} aria-label={backLabel} title={backLabel}>
              <Icon name="back" />
            </button>
            <h2 className={styles.mobileTitle}>{t('admin.dashboard.title')}</h2>
            <div className={styles.headerActions}>
              <LanguageSelector className={styles.lang} />
              <button type="button" className={styles.reports} onClick={() => navigate('/admin/reports')}>
                <BarChart3 aria-hidden />
                <span>{t('admin.dashboard.reports')}</span>
              </button>
              {/* 设置齿轮固定在 header（e2e `data-test="header-settings"`）。 */}
              <button
                type="button"
                className={styles.settingsBtn}
                data-test="header-settings"
                onClick={() => goTab('settings')}
                aria-label={t('admin.dashboard.tabSettings')}
                title={t('admin.dashboard.tabSettings')}
              >
                <Settings aria-hidden />
              </button>
            </div>
          </div>
          {/* 手机顶部导航：高频 4 项 + 「更多」。 */}
          {!desktop && (
            <nav className={styles.nav} aria-label={t('admin.dashboard.navLabel')}>
              <div className={styles.navInner}>
                {MOBILE_TABS.map(id => (
                  <NavItem key={id} id={id} on={tab === id && !moreOpen} onClick={() => goTab(id)} pendingCount={pendingCount} />
                ))}
                <button
                  type="button"
                  className={clsx(styles.tab, moreOpen && styles.tabOn)}
                  aria-expanded={moreOpen}
                  onClick={() => setMoreOpen(v => !v)}
                >
                  <Icon name="chev" />
                  <span>{t('admin.dashboard.more')}</span>
                </button>
              </div>
            </nav>
          )}
        </header>

        <main className={styles.content}>
          {tab === 'kids-status' && <KidsStatusTab onPendingChange={setPendingCount} />}
          {tab === 'chores' && <ChoresTab onOpenCopilot={() => openQuickAssign('copilot')} />}
          {tab === 'templates' && <TemplatesTab />}
          {tab === 'courses' && <CoursesTab />}
          {tab === 'profile' && <ProfileTab />}
          {tab === 'errorbook' && <ErrorbookTab />}
          {tab === 'measures' && <MeasuresTab />}
          {tab === 'approvals' && <ApprovalsTab onCountChange={setPendingCount} />}
          {tab === 'media' && <MediaTab />}
          {tab === 'users' && <UsersTab />}
          {tab === 'rewards' && <RewardsTab />}
          {tab === 'points' && <PointsTab />}
          {tab === 'activity' && <ActivityTab />}
          {tab === 'settings' && <SettingsTab />}
        </main>
      </div>

      {/* 手机「更多」分组抽屉：Sheet 从底部滑出，按 6 组分节列出全部
          14 项（设置在最后）。 */}
      {!desktop && moreOpen && (
        <div className={styles.sheetBackdrop} onClick={() => setMoreOpen(false)} />
      )}
      {!desktop && moreOpen && (
        <div
          className={styles.sheetPanel}
          role="dialog"
          aria-modal="true"
          aria-label={t('admin.dashboard.navLabel')}
        >
          <span className={styles.sheetGrip} aria-hidden />
          <div className={styles.sheetHead}>
            <h2 className={styles.sheetTitle}>{t('admin.dashboard.more')}</h2>
            <button
              type="button"
              className={styles.sheetClose}
              onClick={() => setMoreOpen(false)}
              aria-label={t('common.close')}
            >
              <Icon name="plus" />
            </button>
          </div>
          <div className={styles.sheetBody}>
            {NAV_GROUPS.map(group => (
              <div key={group.label} className={styles.sheetGroup}>
                <p className={styles.sideGroupTitle}>{t(group.label)}</p>
                {group.tabs.map(id => (
                  <NavItem key={id} id={id} on={tab === id} onClick={() => goTab(id)} pendingCount={pendingCount} />
                ))}
              </div>
            ))}
          </div>
        </div>
      )}

      <button
        type="button"
        className={styles.fab}
        onClick={() => openQuickAssign()}
        title={t('admin.dashboard.quickAssign')}
        aria-label={t('admin.dashboard.quickAssign')}
      >
        <Icon name="plus" />
      </button>

      <QuickAssign
        isOpen={quickAssignOpen}
        initialMode={quickAssignMode}
        onClose={() => { setQuickAssignOpen(false); refreshCount(); }}
      />
    </HouseScope>
  );
};

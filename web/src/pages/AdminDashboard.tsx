import React, { useState, useEffect, useCallback } from 'react';
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
import { LanguageSelector } from '../components/LanguageSelector/LanguageSelector';

type Tab = 'kids-status' | 'approvals' | 'chores' | 'templates' | 'courses' | 'profile' | 'errorbook' | 'measures' | 'rewards' | 'points' | 'activity' | 'users' | 'settings';

const TABS: { id: Tab; label: string; icon?: IconName }[] = [
  { id: 'kids-status', label: 'admin.dashboard.tabKids', icon: 'home' },
  { id: 'approvals', label: 'admin.dashboard.tabApprovals', icon: 'check' },
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

/** The four overlapping dots of the House logo, one per person colour. */
const LogoMark: React.FC = () => (
  <span className={styles.logo} aria-hidden>
    <i data-person="coral" /><i data-person="mint" /><i data-person="butter" /><i data-person="sky" />
  </span>
);

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

  useEffect(() => {
    const onHash = () => {
      const next = tabFromHash();
      if (next) {
        setTab(next);
        // Strip the hash so a refresh returns to the default tab and the
        // browser back button doesn't replay tab switches.
        window.history.replaceState(null, '', window.location.pathname);
      }
    };
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

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
      <header className={styles.header}>
        <div className={styles.headerInner}>
          <button type="button" className={styles.back} onClick={() => navigate('/')} aria-label={backLabel} title={backLabel}>
            <Icon name="back" />
          </button>
          <div className={styles.brand}>
            <LogoMark />
            <h1 className={styles.title}>{t('admin.dashboard.title')}</h1>
          </div>
          <div className={styles.headerActions}>
            <LanguageSelector className={styles.lang} />
            <button type="button" className={styles.reports} onClick={() => navigate('/admin/reports')}>
              <BarChart3 aria-hidden />
              <span>{t('admin.dashboard.reports')}</span>
            </button>
          </div>
        </div>
        <nav className={styles.nav} aria-label={t('admin.dashboard.navLabel')}>
          <div className={styles.navInner}>
            {TABS.map(({ id, label, icon }) => (
              <button
                key={id}
                type="button"
                className={clsx(styles.tab, tab === id && styles.tabOn)}
                aria-current={tab === id ? 'page' : undefined}
                onClick={() => setTab(id)}
              >
                {icon ? <Icon name={icon} /> : <Settings aria-hidden />}
                <span>{t(label)}</span>
                {id === 'approvals' && pendingCount > 0 && (
                  <span className={styles.count}>{pendingCount}</span>
                )}
              </button>
            ))}
          </div>
        </nav>
      </header>

      <main className={styles.content}>
        {tab === 'kids-status' && <KidsStatusTab onPendingChange={setPendingCount} />}
        {tab === 'chores' && <ChoresTab />}
        {tab === 'templates' && <TemplatesTab />}
        {tab === 'courses' && <CoursesTab />}
        {tab === 'profile' && <ProfileTab />}
        {tab === 'errorbook' && <ErrorbookTab />}
        {tab === 'measures' && <MeasuresTab />}
        {tab === 'approvals' && <ApprovalsTab onCountChange={setPendingCount} />}
        {tab === 'users' && <UsersTab />}
        {tab === 'rewards' && <RewardsTab />}
        {tab === 'points' && <PointsTab />}
        {tab === 'activity' && <ActivityTab />}
        {tab === 'settings' && <SettingsTab />}
      </main>

      <button
        type="button"
        className={styles.fab}
        onClick={() => setQuickAssignOpen(true)}
        title={t('admin.dashboard.quickAssign')}
        aria-label={t('admin.dashboard.quickAssign')}
      >
        <Icon name="plus" />
      </button>

      <QuickAssign isOpen={quickAssignOpen} onClose={() => { setQuickAssignOpen(false); refreshCount(); }} />
    </HouseScope>
  );
};

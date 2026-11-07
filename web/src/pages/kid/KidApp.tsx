import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, Navigate, Route, Routes, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { SkinScope, TabBar, useMinuteClock, type TabId } from '../../design';
import { useAuth } from '../../AuthContext';
import { useTheme } from '../../ThemeContext';
import { api } from '../../api';
import type { ScheduledChore } from '../../types';
import { useThemeSound } from '../../hooks/useThemeSound';
import { useTextToSpeech } from '../../hooks/useTextToSpeech';
import { useChoreAudio } from '../../hooks/useChoreAudio';
import PinSettingsModal from '../../components/PinPad/PinSettingsModal';
import LinkedAccountsModal from '../../components/LinkedAccounts/LinkedAccountsModal';
import { choreView, gates, isDone, type ChoreView } from './choreView';
import { useKidData } from './useKidData';
import { TopBar, CAT_ORDER } from './parts';
import { HomeScreen } from './HomeScreen';
import { RewardsScreen } from './RewardsScreen';
import { ErrorBookScreen } from './ErrorBookScreen';
import { ReviewScreen } from './ReviewScreen';
import { CourseScreen } from './CourseScreen';
// 042：十学科学习工作区（/learn，主页 LearnEntryCard 进入；不是第 5 个 tab）。
import { LearnScreen } from './LearnScreen';
import { PhotoSheet } from './PhotoSheet';
import { SubmissionSheet } from './SubmissionSheet';
import { MeSheet } from './MeSheet';
import { PointsSheet } from './PointsSheet';
import { CelebrationLayer } from './CelebrationLayer';
import { choreDomId } from './ChoreItem';
import { catFromCategory } from '../../design';
import s from './kid.module.css';

// 四个 tab：主页 / 错题本 / 复习 / 奖励。/week 与 /history 不再是 tab，
// 但路由保留为重定向——旧链接（live 脚本、分享链接）直接落回主页。
const HREFS: Record<TabId, string> = { today: '/', errorbook: '/errorbook', review: '/review', rewards: '/rewards' };

const ttsKey = (id: number) => `openchore_tts_${id}`;
function readTts(id: number, age?: number): boolean {
  try {
    const saved = localStorage.getItem(ttsKey(id));
    if (saved !== null) return saved === '1';
  } catch { /* storage blocked */ }
  // Young readers get chores read aloud by default.
  return age !== undefined && age <= 7;
}

// The key the shared clip player knows a chore row by (matches ChoreItem's).
export const choreSpeakKey = (scheduleId: number, date: string) => `${scheduleId}-${date}`;

interface Celebrating { chore: ScheduledChore; points: number; bonusDrop?: number }

export const KidApp: React.FC = () => {
  const { t } = useTranslation();
  const { user, setUser, signOut, isAdmin, refresh } = useAuth();
  const { skin, color, setSkin, setColor } = useTheme();
  const { playComplete, playAllDone } = useThemeSound();
  const { speak, stop } = useTextToSpeech();
  // One shared audio element for chore clips, with a speaking state the say
  // button reflects — a tap that plays a short clip (or fails) is still
  // visible, instead of reading as "nothing happened".
  const { speakingId, speak: speakClip, stop: stopClip } = useChoreAudio(speak);
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const now = useMinuteClock();

  const tab: TabId = location.pathname.startsWith('/errorbook') ? 'errorbook'
    : location.pathname.startsWith('/review') ? 'review'
    : location.pathname.startsWith('/rewards') ? 'rewards' : 'today';
  // 主页常驻周数据（周历条 / 周小结随主页渲染）；奖励数据仍只在奖励页拉。
  const data = useKidData(user, { week: true, rewards: tab === 'rewards' });

  const [tts, setTts] = useState(() => (user ? readTts(user.id, user.age) : false));
  const [meOpen, setMeOpen] = useState(false);
  // 任务④：积分明细抽屉——顶栏积分胶囊点开，每一分的来历都在里面。
  const [pointsOpen, setPointsOpen] = useState(false);
  const [pinOpen, setPinOpen] = useState(false);
  const [linkedOpen, setLinkedOpen] = useState(false);
  const [photoChore, setPhotoChore] = useState<ScheduledChore | null>(null);
  // 问题①: the chore whose own submission the kid is looking back at.
  const [submissionChore, setSubmissionChore] = useState<ScheduledChore | null>(null);
  const [celebrating, setCelebrating] = useState<Celebrating | null>(null);
  const [baseUrl, setBaseUrl] = useState('');

  // The server remembers the read-aloud preference per person (it used to be
  // per browser): a new device picks it up; the local value stays the
  // instant-response fallback until the fetch lands. null = not set yet
  // (keep the age-based default).
  useEffect(() => {
    if (!user || user.read_aloud == null) return;
    setTts(user.read_aloud);
  }, [user?.id, user?.read_aloud]);

  // The household's public URL for QR links (a grown-up setting; kids fall
  // back to this device's origin).
  useEffect(() => {
    if (!user) return;
    api.admin.getSetting('base_url').then(d => setBaseUrl(d.value)).catch(() => {});
  }, [user]);

  // Back from linking a sign-in (?linked=<provider> or ?auth_error=...).
  useEffect(() => {
    const linked = searchParams.get('linked');
    const authError = searchParams.get('auth_error');
    if (!linked && !authError) return;
    if (linked) {
      refresh();
      data.showToast(t('linkedAccounts.linkedToast'));
    } else if (authError === 'linked_to_other_profile') {
      data.showToast(t('profile.authError.linkedToOther'));
    } else {
      data.showToast(t('linkedAccounts.linkFailed'));
    }
    setSearchParams({}, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams]);

  // --- Chore views ---
  const todayChores = useMemo(
    () => (data.chores ?? []).filter(c => c.date === data.today),
    [data.chores, data.today],
  );
  const { bonusOpen, requiredDone } = gates(todayChores);
  const viewOf = useCallback((c: ScheduledChore): ChoreView => choreView(c, {
    now, today: data.today, bonusOpen, requiredDone, t,
  }), [now, data.today, bonusOpen, requiredDone, t]);

  // The whole day done: the skin's fanfare (not on first load).
  const doneCount = todayChores.filter(isDone).length;
  const allDone = todayChores.length > 0 && doneCount === todayChores.length;
  const prevDone = useRef<boolean | null>(null);
  useEffect(() => {
    if (data.chores == null) return;
    if (prevDone.current === false && allDone) playAllDone();
    prevDone.current = allDone;
  }, [allDone, data.chores, playAllDone]);

  const speakOrPlay = useCallback((text: string, audioUrl?: string, key?: string) => {
    // The chore rows pass their speak key so the right button pulses; the
    // coach note has none and falls back to its text.
    speakClip(key ?? text, text, audioUrl);
  }, [speakClip]);

  const celebrate = useCallback((chore: ScheduledChore, bonusDrop = 0) => {
    playComplete();
    const late = chore.expired && chore.expiry_penalty !== 'block';
    setCelebrating({ chore, points: late ? 0 : chore.points_value || 0, bonusDrop });
  }, [playComplete]);

  const onToggle = useCallback(async (c: ScheduledChore, view: ChoreView) => {
    const result = await data.toggle(c, { retry: view.retry });
    if (result.needsPhoto) setPhotoChore(c);
    if (result.finished) celebrate(c, result.bonusDrop || 0);
  }, [data, celebrate]);

  // The next thing to do after a celebration: the first open chore in order.
  const nextChore = useMemo(() => {
    if (!celebrating) return null;
    for (const cat of CAT_ORDER) {
      for (const c of todayChores) {
        if (catFromCategory(c.category) !== cat || c.schedule_id === celebrating.chore.schedule_id) continue;
        const v = viewOf(c);
        if (v.state === 'todo' && v.canToggle) return c;
      }
    }
    return null;
  }, [celebrating, todayChores, viewOf]);

  const goToChore = (c: ScheduledChore) => {
    if (tab !== 'today') navigate('/');
    requestAnimationFrame(() => {
      const row = document.getElementById(choreDomId(c));
      row?.scrollIntoView({ block: 'center', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
      row?.querySelector<HTMLButtonElement>('.oc-chore__check')?.focus({ preventScroll: true });
    });
  };

  const onPhotoDone = useCallback(async () => {
    const c = photoChore;
    setPhotoChore(null);
    if (!c) return;
    await data.refreshAll();
    if (!c.completed) celebrate(c);
  }, [photoChore, data, celebrate]);

  const setTtsPref = (on: boolean) => {
    if (!user) return;
    setTts(on);
    try { localStorage.setItem(ttsKey(user.id), on ? '1' : '0'); } catch { /* storage blocked */ }
    // Persist across devices too: the preference is per person, not per browser.
    api.users.updateReadAloud(user.id, on).catch(() => {});
    if (!on) { stop(); stopClip(); }
  };

  const logout = async () => {
    await signOut();
    navigate('/login');
  };

  const toManage = () => navigate('/admin/dashboard');

  if (!user) return null;

  const topBar = (
    <TopBar
      user={user}
      balance={data.points?.balance ?? 0}
      onMe={() => setMeOpen(true)}
      onManage={isAdmin ? toManage : undefined}
      onPoints={() => setPointsOpen(true)}
    />
  );
  const shared = { user, data, topBar, viewOf, tts, speakingId, onToggle, onPhoto: setPhotoChore, onViewSubmission: setSubmissionChore, onSpeak: speakOrPlay };

  return (
    <SkinScope skin={skin} color={color} className={s.root}>
      <main className={s.main}>
        <Routes>
          <Route index element={<HomeScreen {...shared} now={now} />} />
          {/* 三页合一后的旧路由：/week 与 /history 重定向回主页（月视图
              展开区承载原历史页内容），旧链接不悬空。 */}
          <Route path="week" element={<Navigate to="/" replace />} />
          <Route path="history" element={<Navigate to="/" replace />} />
          <Route path="errorbook" element={<ErrorBookScreen topBar={topBar} />} />
          <Route path="review" element={<ReviewScreen topBar={topBar} />} />
          <Route path="course/:id" element={<CourseScreen topBar={topBar} />} />
          {/* 042：/learn 路由——学科知识浏览/挑战/再挑战/历史（不入 4 tab）。 */}
          <Route path="learn" element={<LearnScreen topBar={topBar} />} />
          <Route path="rewards" element={<RewardsScreen user={user} data={data} topBar={topBar} onRedeemed={playComplete} />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>

      <TabBar
        active={tab}
        hrefs={HREFS}
        position="fixed"
        className={s.tabs}
        renderLink={({ tab: id, href, ...p }) => <Link key={id} to={href} {...p} />}
      />

      {data.toast && <div className={s.toast} role="status">{data.toast}</div>}

      {meOpen && (
        <MeSheet
          user={user}
          skin={skin}
          isAdmin={isAdmin}
          tts={tts}
          onClose={() => setMeOpen(false)}
          onSkin={(x) => { setSkin(x).catch(() => data.showToast(t('kid.errors.couldntSave'))); }}
          onColor={(c) => { setColor(c).catch(() => data.showToast(t('kid.errors.couldntSave'))); }}
          onAvatar={async (url) => {
            try {
              const updated = await api.users.updateAvatar(user.id, url);
              setUser({ ...user, ...updated });
            } catch (e) {
              console.error('Failed to update avatar:', e);
              data.showToast(t('kid.errors.couldntSave'));
            }
          }}
          onTts={setTtsPref}
          onPin={() => { setMeOpen(false); setPinOpen(true); }}
          onLinked={() => { setMeOpen(false); setLinkedOpen(true); }}
          onManage={toManage}
          onSignOut={logout}
        />
      )}

      {pinOpen && (
        <PinSettingsModal
          userId={user.id}
          hasPin={user.has_pin}
          pinLength={user.pin_length}
          onClose={() => setPinOpen(false)}
          onChanged={(hasPin, pinLength) => setUser({ ...user, has_pin: hasPin, pin_length: pinLength })}
        />
      )}

      {linkedOpen && (
        <LinkedAccountsModal
          user={user}
          self
          onClose={() => setLinkedOpen(false)}
          onChanged={(providers) => setUser({ ...user, auth_providers: providers })}
          onSignedOutEverywhere={logout}
        />
      )}

      {photoChore && (
        <PhotoSheet
          chore={photoChore}
          userId={user.id}
          baseUrl={baseUrl}
          onClose={() => setPhotoChore(null)}
          onComplete={onPhotoDone}
        />
      )}

      {submissionChore && (
        <SubmissionSheet chore={submissionChore} onClose={() => setSubmissionChore(null)} />
      )}

      {pointsOpen && (
        <PointsSheet data={data} onClose={() => setPointsOpen(false)} />
      )}

      {celebrating && (
        <CelebrationLayer
          points={celebrating.points}
          title={celebrating.chore.title}
          streak={data.streak?.current_streak}
          bonusDrop={celebrating.bonusDrop}
          next={nextChore?.title}
          onNext={() => {
            setCelebrating(null);
            if (nextChore) goToChore(nextChore);
          }}
          onBack={() => setCelebrating(null)}
        />
      )}
    </SkinScope>
  );
};

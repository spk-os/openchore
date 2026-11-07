import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Routes, Route, Navigate, useLocation, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from './AuthContext';
import { api } from './api';
import { claimAutoSignIn } from './signInRedirect';
import { ProfileSelection } from './pages/ProfileSelection';
import { Dashboard } from './pages/Dashboard';
import { AmbientDashboard } from './pages/AmbientDashboard';
import { useIdleRedirect } from './hooks/useIdleRedirect';

// Admin screens live inside a parent's own profile: sign in as a parent and
// the "Manage" button appears. The server enforces the role on every call;
// this only keeps non-admins off the pages.
// Dev-only design-system gallery at /design. It renders outside the auth
// gate so it works without the API; the import is dropped from prod builds.
const DesignGallery = import.meta.env.DEV ? React.lazy(() => import('./design/gallery/DesignGallery')) : null;
// Idle-exempt paths: the shared-device timer would otherwise drag someone
// off the page (and fire signOut) mid-task. /boost and /share are token-
// addressed pages a visitor reads without any session, so they exempt too —
// an idle visitor has no session to end.
const IDLE_EXEMPT = import.meta.env.DEV
  ? ['/setup', '/upload', '/design', '/boost', '/share/']
  : ['/setup', '/upload', '/boost', '/share/'];
// 042：公开 taxonomy 报告分享页（token 即凭证，无会话）。
const SharedSnapshotPage = React.lazy(() =>
  import('./taxonomy/TaxonomyWorkspace').then(m => ({ default: m.SharedSnapshotPage })));

// 真实离线 /learn 空页修复：孩子平板离线打开 /learn 时，/auth/me refresh 失败
// 得到 user=null，旧逻辑走 SignedOut → 跳 /login → ProfileSelection 的
// users.list 离线失败 users=[] → return null，#root 一直空白。
// 这里在「离线 + 无 user + 正好是 /learn」这一种组合下不跳登录，直接渲染
// 学科浏览工作区（userId=null：只读浏览；作答/诊断等写操作本来就
// network-only，离线时各自的 UI 报错）。不伪造 user/session；/auth/me 等
// 私有接口从不缓存。复用 App 既有的 TaxonomyWorkspace chunk（同一 module，
// Vite 不会产生重复 chunk）。
const loadTaxonomyWorkspace = () => import('./taxonomy/TaxonomyWorkspace');

// offline→online 的 /auth/me 防抖窗口：浏览器恢复连接后短暂抖动（连上
// 但路由未通）很常见，等这一小段再发请求，同一转变只发一次。
const REFRESH_DEBOUNCE_MS = 300;

// Suspense 只处理 pending；import() reject 是渲染期错误，必须由
// ErrorBoundary 捕获，否则整棵树卸载、#root 空白。兜底文案用固定中文：
// 走到这一步就意味着懒 chunk（连同 i18n 资源）没有被缓存，不能再依赖 t()
// （词条缺失时 i18next 会渲染原始 key，既不中文也可能同样加载失败）。
const OfflineLearnLoadError: React.FC<{ onRetry: () => void }> = ({ onRetry }) => (
  <div data-test="offline-learn-error" role="alert"
    style={{ padding: '10px 12px', borderRadius: 8, background: '#fee2e2', color: '#991b1b', fontSize: 14, lineHeight: 1.6 }}>
    这部分离线内容还没有下载到本机，暂时打不开。
    <button type="button" data-test="offline-learn-retry" onClick={onRetry}
      style={{ marginLeft: 10, padding: '4px 14px', borderRadius: 6, border: '1px solid currentColor', background: 'transparent', color: 'inherit', fontSize: 14, cursor: 'pointer' }}>
      重试
    </button>
  </div>
);

// 懒 chunk 的加载错误边界。重试 = 边界按 attempt 重挂（清掉错误态）+ 每次
// 新建一个 lazy 组件：React.lazy 会缓存已 reject 的 thenable，复用旧实例
// 重试只会立刻再抛同一个错误；新建后才会重新发起 import()。
class OfflineLearnChunkBoundary extends React.Component<
  { onRetry: () => void; children: React.ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }
  render() {
    if (this.state.failed) return <OfflineLearnLoadError onRetry={this.props.onRetry} />;
    return this.props.children;
  }
}

// 离线入口的本地 Suspense fallback：外壳之外的懒 chunk 若尚未被 SW 缓存，
// 不能永久白屏——给一行明确的「正在加载」提示。文案是固定中文的纯 React
// 元素，不走 useTranslation / 远程 i18n：会渲染这个 fallback 就说明懒
// chunk（连同 i18n 词条资源）还没就绪，fallback 若依赖同一条加载链路，
// chunk 失败时它会跟着失败（白屏，或渲染出原始 key）。
const OfflineLearnFallback: React.FC = () => (
  <div data-test="offline-learn-fallback" role="status" style={{ padding: 16 }}>
    正在打开离线学习内容…
  </div>
);

// 离线学科浏览页：离线横幅（仅可看缓存内容，作答等需联网）+ 孩子视角工作区。
const OfflineLearnScreen: React.FC = () => {
  const { t } = useTranslation();
  const online = useOnlineStatus();
  // attempt 每次「重试」递增：丢弃 reject 过的 lazy 实例、重新发起 import()。
  // 仅离线时走这条路（ErrorBoundary 按 attempt 重挂清错误态 + 新建 lazy）。
  const [attempt, setAttempt] = useState(0);
  const OfflineLearnWorkspace = useMemo(
    () => React.lazy(() => loadTaxonomyWorkspace().then(m => ({ default: m.TaxonomyWorkspace }))),
    [attempt],
  );
  // 在线时懒 chunk import() reject 的重试：浏览器把 rejected 的 module 记进
  // module map（负缓存），新建 React.lazy 再 import() 同一 URL 会立刻抛同一
  // 个错——重建实例绕不过去，只有整页 reload 才能让浏览器重新走 SW 缓存/
  // 网络。仅用户手动点击时触发，不自动 reload，不存在循环。
  const retry = () => {
    if (online) {
      window.location.reload();
      return;
    }
    setAttempt(n => n + 1);
  };
  return (
    <div style={{ padding: 16, maxWidth: 680, margin: '0 auto' }}>
      <div data-test="offline-learn-notice" role="status"
        style={{ padding: '10px 12px', borderRadius: 8, background: '#fef9c3', color: '#854d0e', fontSize: 14, lineHeight: 1.6 }}>
        {t('kid.learn.offlineNotice')}
      </div>
      <div style={{ height: 12 }} />
      {/* 边界包 Suspense：import() reject 在 Suspense 之下的渲染期抛出，
          向上冒泡到本边界；pending 阶段则由这个近端 fallback 提示（外层
          App 的 Suspense fallback 是 null，白屏兜不住）。 */}
      <OfflineLearnChunkBoundary key={attempt} onRetry={retry}>
        <React.Suspense fallback={<OfflineLearnFallback />}>
          <OfflineLearnWorkspace audience="kid" userId={null} />
        </React.Suspense>
      </OfflineLearnChunkBoundary>
    </div>
  );
};

// online/offline 事件驱动的连接状态。挂全局监听（不局限于 /learn）：
// offline→online / online→offline 都要触发 App 重算分流；navigator.onLine
// 本身不可响应，必须靠事件翻转这个 state 才能让 SignedOutOrOfflineLearn
// 的条件重新求值。
function useOnlineStatus(): boolean {
  const [online, setOnline] = useState(
    typeof navigator === 'undefined' ? true : navigator.onLine,
  );
  useEffect(() => {
    const up = () => setOnline(true);
    const down = () => setOnline(false);
    window.addEventListener('online', up);
    window.addEventListener('offline', down);
    return () => {
      window.removeEventListener('online', up);
      window.removeEventListener('offline', down);
    };
  }, []);
  return online;
}

// 无 user 时的分流：仅「离线 + /learn」放行只读工作区，其余一律维持原
// SignedOut 行为（在线无 user 照旧跳登录，离线其他路径行为不变）。
// App 层已监听 navigator.onLine 事件并重渲染，这里只读快照，不用再订阅。
const SignedOutOrOfflineLearn: React.FC<{ online: boolean }> = ({ online }) => {
  const location = useLocation();
  const offlineLearn = location.pathname === '/learn' && !online;
  if (!offlineLearn) return <SignedOut />;
  return (
    <OfflineLearnScreen />
  );
};

// The share page reads its token from the URL; App wires the router param
// into the lazy component's prop.
const SharedSnapshotRoute: React.FC = () => {
  const { token } = useParams<{ token: string }>();
  return <SharedSnapshotPage token={token ?? ''} />;
};

// Screens only a parent (or first-run setup) opens load on demand, so a kid's
// tablet doesn't download and parse the whole Manage console to show the picker.
const loadAdmin = () => import('./pages/AdminDashboard');
const loadReports = () => import('./pages/Reports');
const AdminDashboard = React.lazy(() => loadAdmin().then(m => ({ default: m.AdminDashboard })));
const Reports = React.lazy(() => loadReports().then(m => ({ default: m.Reports })));
const SetupWizard = React.lazy(() => import('./pages/SetupWizard').then(m => ({ default: m.SetupWizard })));
const PhotoUpload = React.lazy(() => import('./pages/PhotoUpload').then(m => ({ default: m.PhotoUpload })));
const BoostPage = React.lazy(() => import('./pages/BoostPage').then(m => ({ default: m.BoostPage })));

const RequireAdmin: React.FC<{ children: React.ReactElement }> = ({ children }) => {
  const { user } = useAuth();
  if (!user) return <Navigate to="/login" replace />;
  if (user.role !== 'admin') return <Navigate to="/" replace />;
  return children;
};

// Where someone who opens the app signed out lands: the family picker, the
// wall display, or straight to a sign-in provider (Manage → Settings →
// Sign-in). /login and /ambient always work, so a shared tablet can be
// pointed at either whatever this is set to.
const SignedOut: React.FC = () => {
  const { signInOptions, signedOutHere } = useAuth();
  const location = useLocation();
  const provider = signInOptions.start_page === 'provider' && !signedOutHere ? signInOptions.start_provider : undefined;
  // Decided once per visit, so a re-render doesn't re-check the loop guard.
  const [redirect] = useState(() => !!provider && claimAutoSignIn());

  useEffect(() => {
    if (redirect && provider) {
      window.location.replace(api.auth.oidcLoginURL(provider, undefined, location.pathname + location.search));
    }
  }, [redirect, provider, location.pathname, location.search]);

  if (redirect) return null;
  return <Navigate to={signInOptions.start_page === 'wall' ? '/ambient' : '/login'} replace />;
};

export const App: React.FC = () => {
  const { user, session, isLoading, signOut, refresh } = useAuth();
  const location = useLocation();
  const online = useOnlineStatus();

  // offline→online：重放一次 /auth/me——离线期间的失败把 user 清成了 null，
  // 恢复联网后不刷新就回不了正常会话（不伪造 user，失败仍留在降级 UI）。
  // online→offline 不需要任何网络动作，useOnlineStatus 的 state 变化本身
  // 就会重算下方的分流。
  //
  // 去重（复审缺陷 1）：同一个 online 事件可能被重复派发（浏览器 + 手动
  // dispatch、React 18 并发特性下 effect 重跑），且 effect 依赖 [online,
  // refresh]——任何无关 re-render 都可能重跑 effect。这里保证「一次真实
  // offline→online 转变只发一次 refresh」：
  //  - refreshInFlight：在途去重——上一个 refresh 未 settle 时合并进同一
  //    个 Promise，绝不并发发第二个 /auth/me；
  //  - 防抖 timer：转变后先等 REFRESH_DEBOUNCE_MS 再发，窗口内后续触发
  //    只重置 timer，不叠加请求；offline 时取消待执行 timer（offline 期间
  //    不该发任何网络请求），卸载时一并清理，不留跨卸载的 pending 回调。
  const wasOnline = useRef(online);
  const refreshInFlight = useRef<Promise<void> | null>(null);
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const cameOnline = wasOnline.current === false && online === true;
    wasOnline.current = online;
    if (!cameOnline) return;
    if (refreshTimer.current !== null) clearTimeout(refreshTimer.current);
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      if (refreshInFlight.current) return;
      const p = refresh().catch(() => { /* refresh 内部已兜底：失败保持 user=null */ })
        .finally(() => {
          if (refreshInFlight.current === p) refreshInFlight.current = null;
        });
      refreshInFlight.current = p;
    }, REFRESH_DEBOUNCE_MS);
  }, [online, refresh]);
  // offline 即刻取消待执行的防抖 refresh；卸载清 timer 与在途引用。
  useEffect(() => {
    if (online) return;
    if (refreshTimer.current !== null) {
      clearTimeout(refreshTimer.current);
      refreshTimer.current = null;
    }
  }, [online]);
  useEffect(() => () => {
    if (refreshTimer.current !== null) clearTimeout(refreshTimer.current);
    refreshTimer.current = null;
    refreshInFlight.current = null;
  }, []);

  // 无 user + 离线 + 正好在 /learn：空闲重定向禁用——shared-device 计时器
  // 会把正在看离线学习内容的人拽去 /ambient（还会顺带 signOut）。仅这一种
  // 组合禁用；有 user（正常学习中）或在线页面保持原 idle 行为。
  const idleDisabledForOfflineLearn = !user && !online && location.pathname === '/learn';
  // Shared-device (tap/PIN) sessions end after a few idle minutes and the
  // tablet falls back to the wall display. Personal-device (OIDC) sessions
  // persist.
  useIdleRedirect('/ambient', IDLE_EXEMPT, {
    onIdle: signOut,
    disabled: !!session?.persistent || idleDisabledForOfflineLearn,
  });
  // Fetch Manage and Reports in the background once a parent is in. Router
  // navigations are transitions, so a screen still loading would leave the
  // old one up under the new URL; prefetched, they open at once.
  const isAdmin = user?.role === 'admin';
  useEffect(() => {
    if (!isAdmin) return;
    loadAdmin().catch(() => {});
    loadReports().catch(() => {});
  }, [isAdmin]);

  if (DesignGallery && location.pathname === '/design') {
    return <React.Suspense fallback={null}><DesignGallery /></React.Suspense>;
  }

  if (isLoading) return null;

  return (
    <React.Suspense fallback={null}>
      <Routes>
        <Route path="/login" element={<ProfileSelection />} />
        <Route path="/setup" element={<SetupWizard />} />
        <Route path="/upload" element={<PhotoUpload />} />
        {/* The grandparent's token-addressed page: the token is the auth. */}
        <Route path="/boost/:token" element={<BoostPage />} />
        {/* 042: a shared taxonomy report — token-addressed, public, no session. */}
        <Route path="/share/taxonomy/:token" element={<SharedSnapshotRoute />} />
        <Route path="/admin" element={<Navigate to="/admin/dashboard" replace />} />
        <Route path="/admin/dashboard" element={
          <RequireAdmin><AdminDashboard /></RequireAdmin>
        } />
        <Route path="/admin/reports" element={
          <RequireAdmin><Reports /></RequireAdmin>
        } />
        <Route path="/ambient" element={<AmbientDashboard />} />
        <Route
          path="/*"
          element={user ? <Dashboard /> : <SignedOutOrOfflineLearn online={online} />}
        />
      </Routes>
    </React.Suspense>
  );
};

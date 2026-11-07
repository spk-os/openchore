import React, { createContext, useContext, useState, useEffect, useCallback, useRef } from 'react';
import { api, SESSION_EXPIRED_EVENT } from './api';
import type { AuthSession, SessionInfo, SignInOptions, User } from './types';
import { clearAutoSignIn } from './signInRedirect';
// 042: leaving the app should not keep another person's taxonomy practice
// data in the browser's caches.
import { clearTaxonomyCaches } from './pwa';

interface AuthContextType {
  user: User | null;
  session: SessionInfo | null;
  isLoading: boolean;
  isAdmin: boolean;
  // How the household signs in (Manage → Settings → Sign-in).
  signInOptions: SignInOptions;
  reloadSignInOptions: () => Promise<void>;
  // True after signing out on this page, so the app doesn't send the person
  // straight back to a sign-in provider that might sign them in again.
  signedOutHere: boolean;
  // Adopt a session the server just issued (login, setup). Resolves once
  // the taxonomy caches are cleaned and the session is adopted. Callers
  // MUST await it before navigating (ProfileSelection does), so the cleanup
  // and the state update land before the route change. The boolean says
  // whether the session was actually adopted: false means a newer auth
  // transition (signOut / another signIn / SESSION_EXPIRED) superseded this
  // one during the awaited cleanup, so callers must not navigate as if the
  // sign-in had landed.
  signIn: (auth: AuthSession) => Promise<boolean>;
  // Update the signed-in user's profile after an edit (theme, avatar, PIN...).
  setUser: (user: User) => void;
  // Re-read the session from the server (e.g. after returning from an OIDC
  // provider).
  refresh: () => Promise<void>;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthContextType | undefined>(undefined);

const DEFAULT_SIGN_IN_OPTIONS: SignInOptions = { pin_sign_in: true, start_page: 'picker' };

// The session itself lives in an HttpOnly cookie; this context only mirrors
// what the server reports from /api/auth/me.
export const AuthProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [user, setUserState] = useState<User | null>(null);
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [signInOptions, setSignInOptions] = useState<SignInOptions>(DEFAULT_SIGN_IN_OPTIONS);
  const [signedOutHere, setSignedOutHere] = useState(false);

  // DEF-PWA-PRODUCTION-R2（reviewer P1）：认证状态转换的串行化代数。每次
  // refresh / SESSION_EXPIRED / signIn / signOut 进入异步段前自增，await
  // 之后只有"最新一次操作"才允许写 user/session/signedOutHere——慢的
  // refresh 不能把已经登出/切换的用户复活。ref 而非 state：读的是同步快照，
  // 不触发重渲染。
  const authGeneration = useRef(0);

  // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：signOut 的权威窗口。从
  // signOut 启动到其状态清零落地（或被更新的转换 supersede）为止，窗口内
  // 启动的 refresh 一律让位：logout 请求尚未落地时 cookie 仍有效，me() 会
  // 读回刚被否决的旧 cookie 身份——若放任这个更晚启动的 refresh 自增代数
  // 并采纳身份，signOut 会被"抢走代数"而让位，用户点了登出却仍登录着。
  // 窗口内的 refresh 不自增代数、不清缓存、不写状态，signOut 落地后的下一
  // 次 refresh 恢复正常。
  const signOutInFlight = useRef(false);

  // 042: taxonomy 缓存按会话隔离。登录（signIn）、从登录页把同一个 AuthProvider
  // 挂到另一个人的会话上（用户切换，即同一页面里 user id 变化）、/auth/me 返回
  // 了另一个用户（会话被另一端替换，见 refresh）时清一次，登出（signOut）/
  // SESSION_EXPIRED 同样保留。调用方一律 await：清缓存本身已改为「等 SW ack 后
  // 页面兜底删除」，只有真正 await 才能保证上一用户的缓存先落地删除、新会话
  // 数据再写入，避免「已删缓存被在途写入复活」。清理失败（老浏览器、SW 竞态）
  // 不能阻塞认证流程：本函数内部 catch（clearTaxonomyCaches 已尽力不 reject，
  // 这里是双保险），也绝不在测试或生产里留下 unhandled rejection。
  //
  // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：返回 clearTaxonomyCaches 的
  // 可观测成功布尔——超时/delete 失败/回执缺失不算成功。调用方失败时**不推进
  // lastClearedUserId**（后续 refresh 会因 id 不匹配重试清理），成功才允许推进。
  const clearCachesQuietly = useCallback(async (): Promise<boolean> => {
    try {
      return await clearTaxonomyCaches();
    } catch {
      /* privacy best-effort, never block auth */
      return false;
    }
  }, []);

  // Last user id the caches were cleared for. A ref (not state) so a change
  // never re-renders or re-runs effects; refresh() reads it synchronously,
  // avoiding a stale closure over `user`.
  const lastClearedUserId = useRef<number | null>(null);

  const refresh = useCallback(async () => {
    // signOut 权威窗口内的 refresh 一律让位：此时 logout 未落地、旧 cookie
    // 仍可能有效，me() 读回的是刚被否决的身份；窗口内的 refresh 不自增代
    // 数（不抢走 signOut 的代数）、不清缓存、不写状态。
    if (signOutInFlight.current) return;
    const generation = ++authGeneration.current;
    try {
      const me = await api.auth.me();
      // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：me() 返回后先核对代数
      // ——期间发生 signOut/signIn/SESSION_EXPIRED 的话，本次（慢的）refresh
      // 是过期操作，不得再触发清理、更不得写状态（把已登出/切换前的旧
      // cookie 身份复活）。
      if (authGeneration.current !== generation) return;
      // P2: /auth/me can 200 with a *different* user than we hold — the
      // session cookie was replaced from another tab/device. That is a user
      // switch too: clear the previous person's taxonomy caches *before*
      // adopting the new session (a cache revived by an in-flight write must
      // not outlive the switch), but never let a cleanup failure block the
      // adoption. null → id (the first valid session on this page) clears too:
      // a cold load adopting a cookie session must not inherit whatever an
      // earlier sign-out (this tab or another) failed to wipe. Same user id:
      // no re-clean (avoids a pointless wipe on every re-read).
      if (lastClearedUserId.current !== me.user.id) {
        const cleaned = await clearCachesQuietly();
        // 清理期间又被更新的认证操作 supersede：不得采纳本次结果。
        if (authGeneration.current !== generation) return;
        // 清理失败不推进 lastClearedUserId：下次 refresh（id 仍不匹配）重试，
        // 直到一次可观测成功的清理落地为止。
        if (cleaned) lastClearedUserId.current = me.user.id;
      }
      // 只有仍是最新一次认证操作才写状态：期间发生 signOut/signIn/
      // SESSION_EXPIRED 的话，本次（慢的）refresh 不得复活已被清掉的
      // 用户/会话。
      if (authGeneration.current !== generation) return;
      setUserState(me.user);
      setSession(me.session);
      setSignedOutHere(false);
      clearAutoSignIn();
    } catch {
      // Signed out / unreachable. Only wipe the caches if this page actually
      // held a session (ref set): a first-ever anonymous visitor (null ref)
      // failing offline must not lose the shared/public caches.
      if (authGeneration.current !== generation) return;
      if (lastClearedUserId.current !== null) {
        await clearCachesQuietly();
        if (authGeneration.current !== generation) return;
      }
      setUserState(null);
      setSession(null);
      lastClearedUserId.current = null;
    }
  }, [clearCachesQuietly]);

  const reloadSignInOptions = useCallback(async () => {
    try {
      setSignInOptions(await api.auth.options());
    } catch {
      // Keep the defaults (or the last known options).
    }
  }, []);

  useEffect(() => {
    // Profiles used to be remembered client-side; that key is meaningless now.
    try { localStorage.removeItem('openchore_user'); } catch { /* ignore */ }
    Promise.all([refresh(), reloadSignInOptions()]).finally(() => setIsLoading(false));
  }, [refresh, reloadSignInOptions]);

  useEffect(() => {
    const onExpired = async () => {
      const generation = ++authGeneration.current;
      // Same privacy rule as an explicit sign-out: the session the server
      // just revoked should not leave its taxonomy practice data in the
      // browser's caches. Clean first, then reset the session — awaited so
      // the revocation can't race an in-flight cache write. Only the latest
      // auth transition may reset the state (a slow refresh that resolves
      // afterwards must not resurrect the revoked session).
      await clearCachesQuietly();
      if (authGeneration.current !== generation) return;
      setUserState(null);
      setSession(null);
      lastClearedUserId.current = null;
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
  }, [clearCachesQuietly]);

  const signIn = useCallback(async (auth: AuthSession): Promise<boolean> => {
    const generation = ++authGeneration.current;
    // A login lands here with a brand-new session; a user switch (the same
    // page adopting a different profile, e.g. tap sign-in after idle) lands
    // here too. Either way the previous person's taxonomy caches must not
    // survive into the new session — clean *before* adopting the session so
    // an in-flight cache write can't revive an already-deleted cache. The
    // await is bounded (clearTaxonomyCaches times out on a dead SW), and a
    // cleanup failure never breaks sign-in. Callers must await this promise
    // before navigating (ProfileSelection does). Only the latest auth
    // transition may write the state — a superseded slow signIn must not
    // overwrite a newer signIn/signOut.
    const cleaned = await clearCachesQuietly();
    // 清理期间被更新的认证操作 supersede：不采纳本次会话（返回 false，
    // 调用方不得据此导航）。
    if (authGeneration.current !== generation) return false;
    // 清理失败不推进 lastClearedUserId：后续 refresh（id 不匹配）重试，
    // 直到一次可观测成功的清理落地。
    if (cleaned) lastClearedUserId.current = auth.user.id;
    setUserState(auth.user);
    setSession(auth.session);
    setSignedOutHere(false);
    clearAutoSignIn();
    return true;
  }, [clearCachesQuietly]);

  const setUser = useCallback((u: User) => setUserState(u), []);

  const signOut = useCallback(async () => {
    // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：打开 signOut 权威窗口。
    // 必须在 ++authGeneration 之前置位：窗口检查发生在 refresh 的入口，先
    // 开窗再自增，保证从这一刻起窗口内任何 refresh 都直接让位。finally 关
    // 窗：signOut 无论正常落地还是被更新的转换取代，窗口都必然关闭——
    // 否则被取代的 signOut 会把窗口留成永久开启，后续 refresh 全被挡死。
    signOutInFlight.current = true;
    const generation = ++authGeneration.current;
    try {
      try {
        await api.auth.logout();
      } catch {
        // The cookie is cleared server-side; nothing else to do.
      }
      // Wipe the caches before (and regardless of) the state reset: a failure
      // to clean (older browsers) must not break sign-out, and the awaited
      // cleanup keeps an in-flight cache write from outliving the session.
      await clearCachesQuietly();
      // 只有仍是最新一次认证操作才写状态：登出后到达的慢 refresh/signIn
      // 不得被这次（更慢的）signOut 覆盖，反之被 supersede 的 signOut 也不
      // 得清掉新登录的用户。
      if (authGeneration.current !== generation) return;
      setUserState(null);
      setSession(null);
      setSignedOutHere(true);
      lastClearedUserId.current = null;
    } finally {
      signOutInFlight.current = false;
    }
  }, [clearCachesQuietly]);

  return (
    <AuthContext.Provider value={{
      user, session, isLoading, isAdmin: user?.role === 'admin',
      signInOptions, reloadSignInOptions, signedOutHere,
      signIn, setUser, refresh, signOut,
    }}>
      {children}
    </AuthContext.Provider>
  );
};

export const useAuth = () => {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth must be used within AuthProvider');
  return context;
};

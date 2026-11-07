// 学科学习工作区（卡 TX-FE-CODER）：家长端与孩子端共用的顶层组件。
// 五个标签：浏览（学科知识）、练习、诊断、盲区、历史。宿主（AdminDashboard 的
// TaxonomyTab / KidApp 的入口卡，均为后续集成卡）只传 audience 与 userId，
// 路由/导航/i18n 词条注册不归本文件。
//
// 组合关系：
//   SubjectBrowser（浏览+详情+局部先修图）
//     → PracticeRunner（详情/知识点题库练习）
//     → DiagnosticWorkspace（诊断答题流/结果）
//   BlindspotsWorkspace（盲区再测）→ DiagnosticWorkspace
//   DiagnosticHistory（历史会话）→ DiagnosticWorkspace（续答/结果回看）
//   ExportPanel（历史标签）：GET /api/taxonomy/export 下载 JSON（client.exportData
//   返回 Blob，孩子自己/家长当前孩子由 userId 决定）。
//   浏览器打印在 SharedSnapshotPage；分享 = DiagnosticWorkspace 的 tx-result-share
//   （POST share → /share/taxonomy/{token} 公开页）。

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import type {
  BlindSpotEntry,
  DiagnosticDetail,
  QuestionRow,
  SharedSnapshot,
  TopicDetailResponse,
  TopicRow,
} from './types';
import { taxonomyApi } from './client';
import { blindspotRef } from './normalize';
import { useSubjects } from '../hooks/useSubjects';
import { SubjectBrowser } from './SubjectBrowser';
import { PracticeRunner } from './PracticeRunner';
import { DiagnosticWorkspace, DiagnosticHistory, ResultView } from './DiagnosticWorkspace';
import { BlindspotsWorkspace } from './BlindspotsWorkspace';
import { subjectName } from './labels';
import { EmptyRow, ErrorRow, LoadingRow } from './parts';
import s from './workspace.module.css';

type TabId = 'browse' | 'diagnose' | 'blindspots' | 'history';

/** 课程练习深链上下文：resourceId/courseId 是本地正整数，topicRef 是 external_ref。 */
export interface TaxonomyWorkspaceCourseContext {
  resourceId: number;
  courseId: number;
  topicRef: string;
}

/** 工作区校验后的课程深链：三个字段全部合法才允许消费。 */
type DeepLink = {
  topicRef: string;
  resourceId: number;
  courseId: number;
};

/** 知识点详情按 strict external_ref 寻址：稳定的上游字符串 id，绝不接受本机数字 id。 */
const TOPIC_EXTERNAL_REF_RE = /^[A-Za-z][A-Za-z0-9_.:-]*$/;
export function isTopicExternalRef(value: string): boolean {
  return TOPIC_EXTERNAL_REF_RE.test(value);
}

function isPositiveId(value: number | null | undefined): value is number {
  return Number.isSafeInteger(value) && (value as number) > 0;
}

function detailToTopicRow(detail: TopicDetailResponse, topicRef: string): TopicRow {
  const row = detail?.topic;
  return {
    // PracticeRunner 用 topic.id 发 practice 的 topic_id；深链行必须放 external_ref，
    // 不能把本机自增整数 id 发给按 external_ref 寻址的接口。
    id: topicRef,
    external_ref: topicRef,
    name: row?.name ?? topicRef,
    grade: row?.grade ?? null,
    semester: row?.semester ?? null,
    domain: row?.domain ?? null,
    chapter: row?.chapter ?? null,
    subject: row?.subject ?? null,
    question_count: row?.question_count ?? null,
    description: row?.description ?? null,
    external: row?.external,
  };
}

export interface TaxonomyWorkspaceProps {
  audience: 'kid' | 'parent';
  /** 学习者 id：孩子端 = 自己；家长端 = 当前选中的孩子。 */
  userId?: number | null;
  /** 初始学科；空串 = 等服务端 subjects 到达后自动选首项（绝不回退 math）。 */
  initialSubject?: string;
  /** 深链知识点 external_ref；仅孩子端且带有效 userId 时直接进入练习。 */
  initialTopicRef?: string;
  /** 课程练习深链上下文；非法值由宿主拦截，工作区再校验一次。 */
  courseContext?: TaxonomyWorkspaceCourseContext;
}

type DiagPreset =
  | { kind: 'idle' }
  | { kind: 'targets'; subject: string; targets: TopicRow[] }
  | { kind: 'resume'; sessionId: number; subject?: string };

type PracticePreset =
  | { kind: 'idle' }
  | { kind: 'topic'; subject: string; topic: TopicRow };

/** 学习工作区（双端共用顶层）。 */
export const TaxonomyWorkspace: React.FC<TaxonomyWorkspaceProps> = ({
  audience,
  userId,
  initialSubject = '',
  initialTopicRef,
  courseContext,
}) => {
  const [tab, setTab] = useState<TabId>('browse');
  // 动态学科（卡 TX-COURSE-DEEPLINK-DYNAMIC-CODER）：初始 '' 起步，服务端
  // subjects 到达后若当前值不在列表里则选首项；空列表保持 ''。绝不以 math
  // 兜底——服务端只有 history/biology 或空学科时不得请求/预设数学。
  const { subjects } = useSubjects();
  const [subject, setSubject] = useState(initialSubject);
  useEffect(() => {
    if (subjects.length === 0) return;
    setSubject((cur) => (subjects.some((row) => row.id === cur) ? cur : subjects[0].id));
  }, [subjects]);
  const [diagPreset, setDiagPreset] = useState<DiagPreset>({ kind: 'idle' });
  const [practicePreset, setPracticePreset] = useState<PracticePreset>({ kind: 'idle' });
  const kidTone = audience === 'kid';
  const deepLinkTopicRef = audience === 'kid' && isPositiveId(userId) && initialTopicRef
    && isTopicExternalRef(initialTopicRef)
    ? initialTopicRef
    : null;
  const deepLinkContext = deepLinkTopicRef && courseContext
    && isPositiveId(courseContext.resourceId)
    && isPositiveId(courseContext.courseId)
    && courseContext.topicRef === deepLinkTopicRef
    ? courseContext
    : null;
  const deepLink: DeepLink | null = deepLinkTopicRef && deepLinkContext ? {
    topicRef: deepLinkTopicRef,
    resourceId: deepLinkContext.resourceId,
    courseId: deepLinkContext.courseId,
  } : null;
  const deepLinkKey = deepLink
    ? `${deepLink.topicRef}|${deepLink.resourceId}|${deepLink.courseId}`
    : '';

  type CourseLinkState =
    | { key: string; status: 'idle' }
    | { key: string; status: 'loading' }
    | { key: string; status: 'ready'; topic: TopicRow; questions: QuestionRow[] }
    | { key: string; status: 'exited' }
    | { key: string; status: 'error'; error: unknown };
  const [courseLinkState, setCourseLinkState] = useState<CourseLinkState>(
    () => deepLinkKey ? { key: deepLinkKey, status: 'loading' } : { key: '', status: 'idle' },
  );
  // 重试计数：递增即重跑深链 effect（绕过 cancelled 标记重新发起请求）。
  const [courseLinkAttempt, setCourseLinkAttempt] = useState(0);
  const activeCourseLink = deepLinkKey && courseLinkState.key === deepLinkKey
    ? courseLinkState
    : null;
  const exitCourseLink = useCallback(() => {
    setCourseLinkState((prev) => ({ key: prev.key, status: 'exited' }));
  }, []);
  const retryCourseLink = useCallback(() => {
    setCourseLinkAttempt((n) => n + 1);
  }, []);
  // 浏览/诊断共用同一 subject；用户在浏览页切学科时同步顶层诊断预设。
  const onSubjectChange = useCallback((next: string) => {
    setSubject(next);
  }, []);

  // 课程练习深链：按 external_ref 拉详情后直接进真实练习。参数变化、重试或
  // 卸载时 cancelled 让旧响应失效；state.key 也拦住尚未清理的旧参数结果。
  useEffect(() => {
    if (!deepLink || !deepLinkKey) {
      setCourseLinkState({ key: '', status: 'idle' });
      return;
    }
    let cancelled = false;
    const requestKey = deepLinkKey;
    setPracticePreset({ kind: 'idle' });
    setCourseLinkState({ key: requestKey, status: 'loading' });
    setTab('browse');
    taxonomyApi
      .topicDetail(deepLink.topicRef)
      .then((detail) => {
        if (cancelled) return;
        setCourseLinkState({
          key: requestKey,
          status: 'ready',
          topic: detailToTopicRow(detail, deepLink.topicRef),
          questions: detail?.questions ?? [],
        });
      })
      .catch((err) => {
        if (!cancelled) setCourseLinkState({ key: requestKey, status: 'error', error: err });
      });
    return () => {
      cancelled = true;
    };
  }, [deepLinkKey, courseLinkAttempt]);

  const TABS: { id: TabId; label: string }[] = useMemo(
    () => [
      { id: 'browse', label: kidTone ? '学科知识' : '学科图谱' },
      { id: 'diagnose', label: kidTone ? '挑战检测' : '诊断' },
      { id: 'blindspots', label: kidTone ? '再挑战' : '盲区' },
      { id: 'history', label: '历史' },
    ],
    [kidTone],
  );

  // 「诊断这个知识点」：带着目标切到诊断标签。
  const onDiagnose = useCallback((topic: TopicRow) => {
    setDiagPreset({ kind: 'targets', subject: topic.subject ?? subject, targets: [topic] });
    setTab('diagnose');
  }, [subject]);

  // 「练习」：详情页题目拉取后进练习（PracticeRunner 自己处理空题库）。
  const onPractice = useCallback((topic: TopicRow) => {
    setPracticePreset({ kind: 'topic', subject: topic.subject ?? subject, topic });
    setTab('browse');
  }, [subject]);

  const onRetest = useCallback((entries: BlindSpotEntry[]) => {
    if (entries.length === 0) return;
    setDiagPreset({
      kind: 'targets',
      subject: entries[0].subject ?? subject,
      targets: entries.map((e) => {
        // 再测目标必须以 external_ref 寻址（StartPanel 发 targets 给后端）。
        const ref = blindspotRef(e) ?? '';
        return {
          id: ref,
          external_ref: ref,
          name: e.name ?? ref,
          grade: e.grade ?? null,
          semester: null,
          domain: null,
          chapter: e.chapter ?? null,
          subject: e.subject ?? subject,
        };
      }),
    });
    setTab('diagnose');
  }, [subject]);

  const onOpenTopic = useCallback((_ref: string) => {
    // 祖先知识点点按：切回浏览页（学科/章目录自持，无需带 ref 深跳）。
    setPracticePreset({ kind: 'idle' });
    setTab('browse');
  }, []);

  return (
    <div className={s.screen} data-test="tx-workspace">
      <div className={s.subjectChips} role="tablist" aria-label="学习工作区">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            className={s.subjectChip}
            data-test={`tx-tab-${t.id}`}
            aria-pressed={tab === t.id}
            onClick={() => setTab(t.id)}
          >
            {t.label}
          </button>
        ))}
      </div>
      {/* 课程练习深链的返回门：仅深链就绪时显示，onExit 回原课程。 */}
      {deepLink && activeCourseLink?.status === 'ready' && (
        <div className={s.detailCard} data-test="tx-course-back-panel">
          <Link
            className={s.btn}
            data-test="tx-course-back"
            data-testid="tx-course-back"
            to={`/course/${deepLink.courseId}`}
          >
            返回课程
          </Link>
        </div>
      )}

      {/* FR-12c 打印友好视图：诊断报告/学习路径（诊断标签）与盲区清单
       *（盲区标签）共用屏幕排版，打印态由 @media print 隐藏交互控件并
       * 控制分页。浏览与历史标签是交互工作流，不提供打印按钮。 */}
      {(tab === 'diagnose' || tab === 'blindspots') && (
        <button
          type="button"
          className={`${s.btnPrimary} ${s.noPrint}`}
          data-test="tx-print"
          data-testid="tx-print"
          onClick={() => window.print()}
        >
          打印 / 另存为 PDF
        </button>
      )}

      {tab === 'browse' && (
        deepLink ? (
          activeCourseLink?.status === 'error' ? (
            <ErrorRow
              error={activeCourseLink.error}
              onRetry={retryCourseLink}
              test="tx-course-link-error"
            />
          ) : activeCourseLink?.status === 'ready' ? (
            <PracticeRunner
              topic={activeCourseLink.topic}
              questions={activeCourseLink.questions}
              userId={userId}
              onExit={exitCourseLink}
              courseContext={{ resourceId: deepLink.resourceId, topicRef: deepLink.topicRef }}
            />
          ) : activeCourseLink?.status === 'exited' ? (
            <SubjectBrowser
              initialSubject={subject}
              initialTopicRef={deepLinkTopicRef ?? undefined}
              onSubjectChange={onSubjectChange}
              onDiagnose={onDiagnose}
              onPractice={onPractice}
            />
          ) : (
            <LoadingRow label="正在打开课程练习…" />
          )
        ) : practicePreset.kind === 'topic' ? (
          <TopicPracticePanel
            topic={practicePreset.topic}
            userId={userId}
            onExit={() => setPracticePreset({ kind: 'idle' })}
          />
        ) : (
          <SubjectBrowser
            initialSubject={subject}
            initialTopicRef={deepLinkTopicRef ?? undefined}
            onSubjectChange={onSubjectChange}
            onDiagnose={onDiagnose}
            onPractice={onPractice}
          />
        )
      )}

      {tab === 'diagnose' &&
        (diagPreset.kind === 'targets' ? (
          <DiagnosticWorkspace
            userId={userId}
            audience={audience}
            presetTargets={diagPreset.targets}
            presetSubject={diagPreset.subject}
            onGenerateCourse={
              audience === 'parent'
                ? async (sessionId) => {
                    await generateCourseFromSession(sessionId, userId, diagPreset.subject, subjectName(diagPreset.subject));
                  }
                : undefined
            }
          />
        ) : diagPreset.kind === 'resume' ? (
          <DiagnosticWorkspace
            userId={userId}
            audience={audience}
            resumeSessionId={diagPreset.sessionId}
            presetSubject={diagPreset.subject ?? subject}
          />
        ) : (
          <DiagnosticWorkspace userId={userId} audience={audience} presetSubject={subject} />
        ))}

      {tab === 'blindspots' && (
        <BlindspotsWorkspace userId={userId} audience={audience} onRetest={onRetest} onOpenTopic={onOpenTopic} />
      )}

      {tab === 'history' && (
        <>
          <ExportPanel userId={userId} audience={audience} />
          <DiagnosticHistory
            userId={userId}
            onOpen={(sessionId) => {
              setDiagPreset({ kind: 'resume', sessionId });
              setTab('diagnose');
            }}
          />
        </>
      )}
    </div>
  );
};

/** 详情页「练习」面板：拉题库再进 PracticeRunner。 */
const TopicPracticePanel: React.FC<{
  topic: TopicRow;
  userId?: number | null;
  onExit: () => void;
}> = ({ topic, userId, onExit }) => {
  const [error, setError] = useState<unknown>(null);

  const [qRows, setQRows] = useState<import('./types').QuestionRow[] | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    taxonomyApi
      .topicDetail(topic.id)
      .then((d) => {
        if (!cancelled) setQRows(d.questions ?? []);
      })
      .catch((err) => {
        if (!cancelled) setError(err);
      });
    return () => {
      cancelled = true;
    };
  }, [topic.id]);

  if (error) return <ErrorRow error={error} onRetry={onExit} />;
  if (!qRows) return <LoadingRow />;
  return (
    <PracticeRunner
      topic={topic}
      questions={qRows}
      userId={userId}
      onExit={onExit}
    />
  );
};

/** 家长端：按诊断学习路径生成 draft 课程（课程生成入口）。 */
async function generateCourseFromSession(
  sessionId: number,
  userId: number | null | undefined,
  subject: string,
  subjectZh: string,
): Promise<void> {
  if (userId == null) return;
  await taxonomyApi.generateCourse({
    user_id: userId,
    title: `${subjectZh}诊断课程`,
    subject,
    diagnostic_session_id: sessionId,
  });
}

/** 导出学习数据 JSON 并触发浏览器下载（与 ExportConfigSection 同款 Blob 下载）。 */
function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  // Safari 在 click 返回后才开始读 blob，立即 revoke 会截断下载；
  // 延迟释放（Chromium/Firefox 同样安全，URL 存活 10s 足够完成下载）。
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

/** 历史标签的导出面板（卡 TX-FE-EXPORT）：下载当前学习者的完整学习数据 JSON。 */
const ExportPanel: React.FC<{
  userId?: number | null;
  audience: 'kid' | 'parent';
}> = ({ userId, audience }) => {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [doneAt, setDoneAt] = useState<number | null>(null);
  const kidTone = audience === 'kid';

  const download = async () => {
    setBusy(true);
    setError(null);
    setDoneAt(null);
    try {
      const blob = await taxonomyApi.exportData(userId ?? undefined);
      downloadBlob(blob, `taxonomy-export${userId != null ? `-${userId}` : ''}.json`);
      setDoneAt(Date.now());
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={s.detailCard} data-test="tx-export-panel">
      <button
        type="button"
        className={s.btn}
        data-test="tx-export-download"
        disabled={busy}
        onClick={download}
      >
        {busy ? '导出中…' : kidTone ? '保存我的学习记录' : '导出学习数据（JSON）'}
      </button>
      {doneAt != null && <span className={s.muted}>已下载。</span>}
      {error ? (
        <ErrorRow error={error} test="tx-export-error" onRetry={download} />
      ) : null}
    </div>
  );
};

/** 公开分享报告页（/share/taxonomy/{token}，无会话）。
 *  GET /api/taxonomy/shared/{token} 返回 {report_kind, expires_at, snapshot}
 *  wrapper —— 这里 unwrap snapshot 后渲染，wrapper 不是扁平快照。 */
export const SharedSnapshotPage: React.FC<{ token: string }> = ({ token }) => {
  const [snapshot, setSnapshot] = useState<SharedSnapshot | null>(null);
  const [error, setError] = useState<unknown>(null);

  React.useEffect(() => {
    let cancelled = false;
    taxonomyApi
      .sharedReport(token)
      .then((report) => {
        if (cancelled) return;
        // 服务端快照本体缺失/为空：按「链接不存在或已失效」处理，
        // 不渲染半张报告。
        const snap = report?.snapshot;
        if (!snap || typeof snap !== 'object') {
          setError(new Error('分享链接不存在、已过期或已被撤销。'));
          return;
        }
        setSnapshot(snap);
      })
      .catch((err) => {
        if (!cancelled) setError(err);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  if (error) {
    const gone = error instanceof Error && /不存在|过期|撤销|404|410/.test(error.message);
    return (
      <EmptyRow test="tx-share-missing">
        {gone ? '分享链接不存在、已过期或已被撤销。' : '分享页打开失败，请稍后再试。'}
      </EmptyRow>
    );
  }
  if (!snapshot) return <LoadingRow />;

  // 快照本体带 summary（诊断报告的服务端形状：{session_id, subject,
  // mode, created_at, summary}）；没有 summary 的旧快照按 stats 兜底拼装。
  const snapSummary = snapshot.summary ?? null;
  const fallbackWeak = (snapshot.weak ?? []).map((w) => (typeof w === 'string' ? w : w.id)).filter((v): v is string => typeof v === 'string');
  const fallbackShaky = (snapshot.shaky ?? []).map((w) => (typeof w === 'string' ? w : w.id)).filter((v): v is string => typeof v === 'string');
  const summary: import('./types').DiagnosticSummary = snapSummary
    ? snapSummary
    : {
        mode: snapshot.mode ?? 'screen',
        asked: snapshot.stats?.asked ?? null,
        weak: fallbackWeak,
        shaky: fallbackShaky,
        mastered: [],
        untested: snapshot.stats?.untested != null ? [] : [],
        roots: (snapshot.roots ?? []).map((r) => ({
          id: r.id,
          name: r.name ?? r.id,
          pending_prereqs: r.pending_prereqs ?? null,
          out_of_scope_prereqs: r.out_of_scope_prereqs ?? null,
          external_prereqs: r.external_prereqs ?? null,
        })),
        path: snapshot.path ?? null,
        chapters: snapshot.chapters ?? null,
        limit_reached: false,
        finished: true,
      };

  const fakeDetail: DiagnosticDetail = {
    session: {
      id: snapshot.session_id ?? 0,
      subject: snapshot.subject ?? '',
      mode: snapshot.mode ?? 'screen',
      status: 'completed',
      label: snapshot.label ?? snapshot.headline ?? '学习报告',
    },
    summary,
    attempts: snapshot.answers ?? null,
  };

  return (
    <div className={s.screen} data-test="tx-shared">
      <div className={s.screenHead}>
        <h1 className={s.title}>{snapshot.label ?? '学习报告'}</h1>
        {snapshot.user_name && <p className={s.subtitle}>{snapshot.user_name}</p>}
      </div>
      <ResultView detail={fakeDetail} audience="parent" />
      <button type="button" className={s.btnPrimary} onClick={() => window.print()}>
        打印 / 另存为 PDF
      </button>
    </div>
  );
};

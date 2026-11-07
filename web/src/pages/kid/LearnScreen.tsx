// The kid's learn screen (042): the ten-subject knowledge workspace —
// browse, challenge (diagnose), re-challenge (blindspots) and history, in
// the shared TaxonomyWorkspace with the kid tone. The kid is always the
// learner (their own id); the back door returns to Today.
import { Link, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from '../../AuthContext';
import {
  TaxonomyWorkspace,
  isTopicExternalRef,
  type TaxonomyWorkspaceCourseContext,
} from '../../taxonomy/TaxonomyWorkspace';
import s from './kid.module.css';

const POSITIVE_ID_RE = /^[1-9]\d*$/;
const INVALID_LINK_MESSAGE = '课程练习链接无效，请从课程页重新打开练习。Invalid course practice link.';

/** 仅接受无前导零、无空白、无符号且处于安全整数范围的正整数。 */
function parsePositiveId(value: string | null): number | null {
  if (value === null || !POSITIVE_ID_RE.test(value)) return null;
  const n = Number(value);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

type LearnLinkState =
  | { kind: 'browse' }
  | { kind: 'topic'; topicRef: string }
  | { kind: 'course'; topicRef: string; courseContext: TaxonomyWorkspaceCourseContext }
  | { kind: 'error' };

/** 课程练习深链：topic 必须恰一次且 trim 后仍是 external_ref；课程 id 必须成对且严格正整数。 */
function parseLearnLink(params: URLSearchParams): LearnLinkState {
  const topicValues = params.getAll('topic');
  const resourceValues = params.getAll('resourceId');
  const courseValues = params.getAll('courseId');
  if (topicValues.length > 1 || resourceValues.length > 1 || courseValues.length > 1) {
    return { kind: 'error' };
  }
  const hasResource = resourceValues.length === 1;
  const hasCourse = courseValues.length === 1;
  if (hasResource !== hasCourse) return { kind: 'error' };

  // trim 后校验：空白包裹的 ref 规整后再过 external_ref 规则（纯数字本机 id 仍被拒）。
  const rawTopic = topicValues.length === 1 ? topicValues[0].trim() : null;
  if (rawTopic !== null && !isTopicExternalRef(rawTopic)) return { kind: 'error' };

  if (!hasResource) {
    return rawTopic === null
      ? { kind: 'browse' }
      : { kind: 'topic', topicRef: rawTopic };
  }
  const resourceId = parsePositiveId(resourceValues[0]);
  const courseId = parsePositiveId(courseValues[0]);
  if (rawTopic === null || resourceId === null || courseId === null) {
    return { kind: 'error' };
  }
  return {
    kind: 'course',
    topicRef: rawTopic,
    courseContext: { resourceId, courseId, topicRef: rawTopic },
  };
}

export function LearnScreen({ topBar }: { topBar: React.ReactNode }) {
  const { t } = useTranslation();
  const { user } = useAuth();
  const [params] = useSearchParams();
  const link = parseLearnLink(params);
  const initialTopicRef = link.kind === 'topic' || link.kind === 'course'
    ? link.topicRef
    : undefined;
  const courseContext = link.kind === 'course' ? link.courseContext : undefined;
  // 非法参数不把任何深链字段下传：工作区保持普通浏览，绝不会形成 courseContext。
  const workspaceProps = {
    audience: 'kid' as const,
    userId: user?.id ?? null,
    ...(initialTopicRef !== undefined ? { initialTopicRef } : {}),
    ...(courseContext !== undefined ? { courseContext } : {}),
  };

  return (
    <div className={s.ebScreen} data-test="kid-learn-page">
      {topBar}
      <h1 className={s.screenTitle}>{t('kid.learn.title')}</h1>
      {link.kind === 'error' && (
        <p
          className={s.ebError}
          data-test="learn-invalid-course"
          data-testid="learn-invalid-course"
          role="alert"
          aria-live="assertive"
        >
          {INVALID_LINK_MESSAGE}
        </p>
      )}
      <TaxonomyWorkspace {...workspaceProps} />
      <p className={s.ebFoot} data-test="learn-back">
        <Link to="/">{t('kid.learn.backToday')}</Link>
      </p>
    </div>
  );
}

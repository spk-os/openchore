// 学科纯标签 helper（任务卡 TX-SUBJECTS-HELPERS-CODER）。
//
// 本文件不再持有任何学科词表：学科列表的唯一运行时来源是 useSubjects
// （服务端 GET /api/taxonomy/subjects，subjects 表驱动），纯函数不能调
// hook，所以这里只做「单值 → 文案」的无词表映射：
//  - 非空学科统一拼 admin.analysis.subject_* 词条族，i18n 有词条就走词条，
//    没有词条（t 返回 key 原文）就回退学科原始字符串；
//  - 空串（''，非学科任务）仍由调用方通过 unclassifiedKey 指定文案。
// SUBJECT_ORDER / ALL_SUBJECTS / isKnownSubject 硬编码已删除——运行时
// 出现的学科以服务端返回为准，任何组件不得再迭代本模块的静态清单。

/** 学科 key：开放 string（服务端 subjects 表驱动，不由前端词表封死）。 */
export type SubjectKey = string;

/**
 * 学科 → i18n key：非空学科一律生成 admin.analysis.subject_* 词条族 key。
 * 词条是否存在由 i18n 词条表决定，本函数不做词表校验。
 */
export function subjectLabelKey(subject: string): string {
  return subject === '' ? subject : `admin.analysis.subject_${subject}`;
}

/**
 * 学科选项文案：'' 用 unclassifiedKey，非空学科优先用服务端 SubjectRow.name
 * （可选参数，调用方从 useSubjects 拿到的行直接传入；缺省/空串/空白时回落
 * admin.analysis.subject_* 词条），词条缺失（t 返回 key 原文）再回退学科原始
 * 字符串。冻结签名 subjectOptionLabel(t, subject, unclassifiedKey) 不变，
 * 第 4 参纯增量。
 */
export function subjectOptionLabel(
  t: (key: string) => string,
  subject: string,
  unclassifiedKey: string,
  serverName?: string,
): string {
  if (subject === '') return t(unclassifiedKey);
  const trimmed = serverName?.trim();
  if (trimmed) return trimmed;
  const key = `admin.analysis.subject_${subject}`;
  const label = t(key);
  return label === key ? subject : label;
}

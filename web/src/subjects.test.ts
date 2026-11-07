// 纯函数测试（node 环境）：学科标签 helper（任务卡 TX-SUBJECTS-HELPERS-TESTER）。
// subjects.ts 不持有学科词表：此处只用伪 t 验证「有词条走词条 / 无词条回退原值」，
// 以及 '' 走 unclassifiedKey；不重建任何静态学科清单或顺序断言。
import { describe, expect, it } from 'vitest';
import { subjectLabelKey, subjectOptionLabel } from './subjects';
import zh from './i18n/locales/zh/translation.json';

/** 十学科 key（仅作为测试输入，非业务词表；顺序无关）。 */
const SUBJECT_KEYS = [
  'math',
  'chinese',
  'english',
  'physics',
  'chemistry',
  'science',
  'morality',
  'history',
  'geography',
  'biology',
];

/** 从真实词典里查 key；缺失时返回 key 原文（与 i18next 缺词条行为一致）。 */
function dictT(dict: Record<string, unknown>) {
  return (key: string): string => {
    const parts = key.split('.');
    let node: unknown = dict;
    for (const p of parts) {
      if (node && typeof node === 'object' && p in (node as Record<string, unknown>)) {
        node = (node as Record<string, unknown>)[p];
      } else {
        return key;
      }
    }
    return typeof node === 'string' ? node : key;
  };
}

const UNCLASSIFIED = 'admin.analysis.unclassified';

describe('subjectLabelKey', () => {
  it('非空学科拼 admin.analysis.subject_* 词条 key', () => {
    for (const s of SUBJECT_KEYS) {
      expect(subjectLabelKey(s)).toBe(`admin.analysis.subject_${s}`);
    }
  });
  it("'' 返回 ''（空串不生成词条 key）", () => {
    expect(subjectLabelKey('')).toBe('');
  });
  it('不做固定词表校验：未知 key 也照样生成词条 key，不抛错', () => {
    expect(subjectLabelKey('astronomy')).toBe('admin.analysis.subject_astronomy');
    expect(subjectLabelKey('Math-2')).toBe('admin.analysis.subject_Math-2');
  });
});

describe('subjectOptionLabel - 词条命中', () => {
  const t = dictT(zh as unknown as Record<string, unknown>);
  it('十学科 key 都能翻译（不返回 key 原文）', () => {
    for (const s of SUBJECT_KEYS) {
      const label = subjectOptionLabel(t, s, UNCLASSIFIED);
      expect(label).not.toBe(s);
      expect(label).not.toBe(subjectLabelKey(s));
      expect(label.length).toBeGreaterThan(0);
    }
    expect(subjectOptionLabel(t, 'math', UNCLASSIFIED)).toBe('数学');
    expect(subjectOptionLabel(t, 'biology', UNCLASSIFIED)).toBe('生物');
  });
  it("'' 用 unclassifiedKey 查词条", () => {
    const fakeT = (key: string) => (key === UNCLASSIFIED ? '未分学科' : key);
    expect(subjectOptionLabel(fakeT, '', UNCLASSIFIED)).toBe('未分学科');
  });
});

describe('subjectOptionLabel - 回退', () => {
  it('无词条（t 返回 key 原文）时回退学科原始值', () => {
    const missingT = (key: string) => key;
    expect(subjectOptionLabel(missingT, 'astronomy', UNCLASSIFIED)).toBe('astronomy');
    expect(subjectOptionLabel(missingT, 'math', UNCLASSIFIED)).toBe('math');
  });
  it('真实词典缺该学科词条时回退原值', () => {
    const t = dictT(zh as unknown as Record<string, unknown>);
    expect(subjectOptionLabel(t, 'astronomy', UNCLASSIFIED)).toBe('astronomy');
  });
  it("'' 且 unclassifiedKey 无词条时回退为 key 原文（不误回退成空串学科）", () => {
    const missingT = (key: string) => key;
    expect(subjectOptionLabel(missingT, '', UNCLASSIFIED)).toBe(UNCLASSIFIED);
  });
});

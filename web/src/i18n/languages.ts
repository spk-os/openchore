export interface LanguageOption {
  /** BCP-47 / i18next language code, e.g. 'en', 'fr'. */
  code: string;
  /** Native display name shown in the selector. */
  label: string;
}

// Single source of truth. Adding a language = add one entry here and drop a
// matching locales/<code>/translation.json file (see web/src/i18n/index.ts).
export const LANGUAGES: LanguageOption[] = [
  { code: 'zh', label: '中文' },
  { code: 'en', label: 'English' },
  { code: 'de', label: 'Deutsch' },
];

export const SUPPORTED_CODES = LANGUAGES.map((l) => l.code);
// 中文是主要用户语言；英语仅作为缺失 key 时的回退。
export const FALLBACK_CODE = 'zh';

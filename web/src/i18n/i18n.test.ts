import { describe, it, expect, beforeAll } from 'vitest';
import i18n from './index';

beforeAll(async () => {
  if (!i18n.isInitialized) {
    await i18n.init();
  }
});

describe('i18n configuration', () => {
  it('resolves a known key to its English string', async () => {
    await i18n.changeLanguage('en');
    expect(i18n.t('common.back')).toBe('Back');
  });

  it('falls back to the default language for an unsupported language', async () => {
    await i18n.changeLanguage('zz-ZZ');
    expect(i18n.resolvedLanguage).toBe('zh');
    expect(i18n.t('common.save')).toBe('保存');
  });

  it('interpolates variables', async () => {
    await i18n.changeLanguage('en');
    expect(i18n.t('test.greeting', { name: 'Sam' })).toBe('Hi, Sam');
    await i18n.changeLanguage('zh');
    expect(i18n.t('test.greeting', { name: 'Sam' })).toBe('你好，Sam');
  });

  it('selects singular vs plural', async () => {
    await i18n.changeLanguage('en');
    expect(i18n.t('test.points', { count: 1 })).toBe('1 point');
    expect(i18n.t('test.points', { count: 3 })).toBe('3 points');
    // Chinese has a single plural category: both counts share the _other form.
    await i18n.changeLanguage('zh');
    expect(i18n.t('test.points', { count: 1 })).toBe('1 分');
    expect(i18n.t('test.points', { count: 3 })).toBe('3 分');
  });
});

import { afterEach, describe, expect, test, vi } from 'vitest'
import i18n, { LANG_STORAGE_KEY, initialLang, normalizeLang, setLanguage } from './index'
import en from './locales/en'
import zhCN from './locales/zh-CN'

afterEach(async () => {
  localStorage.clear()
  vi.unstubAllGlobals()
  await i18n.changeLanguage('en')
})

describe('normalizeLang', () => {
  // One Chinese bundle serves every Chinese tag a browser may report.
  test.each(['zh', 'zh-CN', 'zh-TW', 'zh-Hans', 'ZH-cn'])('%s resolves to zh-CN', (tag) => {
    expect(normalizeLang(tag)).toBe('zh-CN')
  })

  test.each(['en', 'en-GB', 'de', 'ja', '', undefined, null])(
    '%s resolves to English',
    (tag) => {
      expect(normalizeLang(tag)).toBe('en')
    },
  )
})

describe('initialLang', () => {
  test('a browser with no Chinese language gets English', () => {
    vi.stubGlobal('navigator', { ...navigator, language: 'de-DE' })
    expect(initialLang()).toBe('en')
  })

  test('a Chinese browser gets zh-CN without any stored choice', () => {
    vi.stubGlobal('navigator', { ...navigator, language: 'zh-CN' })
    expect(initialLang()).toBe('zh-CN')
  })

  // The stored choice is the only signal that says what this person picked,
  // rather than how their OS happened to be installed.
  test('a stored choice beats the browser language', () => {
    localStorage.setItem(LANG_STORAGE_KEY, 'en')
    vi.stubGlobal('navigator', { ...navigator, language: 'zh-CN' })
    expect(initialLang()).toBe('en')
  })
})

describe('resources', () => {
  test('English is the active language by default', () => {
    expect(i18n.language).toBe('en')
    expect(i18n.t('nav.publish')).toBe(en.nav.publish)
  })

  test('setLanguage switches the bundle and persists the choice', async () => {
    setLanguage('zh-CN')
    await i18n.changeLanguage('zh-CN')
    expect(i18n.t('nav.publish')).toBe(zhCN.nav.publish)
    expect(localStorage.getItem(LANG_STORAGE_KEY)).toBe('zh-CN')
  })
})

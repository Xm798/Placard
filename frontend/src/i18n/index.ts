import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import en from './locales/en'
import zhCN from './locales/zh-CN'

export type Lang = 'en' | 'zh-CN'

export const LANGUAGES: { code: Lang; label: string }[] = [
  { code: 'en', label: en.languageName },
  { code: 'zh-CN', label: zhCN.languageName },
]

export const LANG_STORAGE_KEY = 'placard-lang'

// Every Chinese tag a browser may report — zh, zh-CN, zh-TW, zh-Hans — resolves
// to the one Chinese bundle there is; everything else is English.
export function normalizeLang(raw?: string | null): Lang {
  return raw?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

// A stored choice wins over the browser's own languages: it is the only signal
// that says what this person picked rather than what their OS was installed as.
// Storage can throw outright (Safari private mode, site data blocked), which is
// not a reason to fail loading the app.
function storedLang(): Lang | null {
  try {
    const saved = localStorage.getItem(LANG_STORAGE_KEY)
    return saved ? normalizeLang(saved) : null
  } catch {
    return null
  }
}

export function initialLang(): Lang {
  return storedLang() ?? normalizeLang(navigator.language)
}

void i18n.use(initReactI18next).init({
  resources: {
    en: { translation: en },
    'zh-CN': { translation: zhCN },
  },
  lng: initialLang(),
  fallbackLng: 'en',
  // React already escapes what it renders.
  interpolation: { escapeValue: false },
})

// <html lang> is what a screen reader picks its voice from and what the
// browser's own "translate this page?" prompt reads, so it tracks the bundle
// actually in use rather than whatever index.html shipped with.
function applyDocumentLang(lang: string) {
  document.documentElement.lang = lang
}

applyDocumentLang(i18n.language)
i18n.on('languageChanged', applyDocumentLang)

export function setLanguage(lang: Lang): void {
  try {
    localStorage.setItem(LANG_STORAGE_KEY, lang)
  } catch {
    // Unavailable storage costs the choice its persistence, not its effect.
  }
  void i18n.changeLanguage(lang)
}

export default i18n

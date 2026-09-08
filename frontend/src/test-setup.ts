import '@testing-library/jest-dom'
import { beforeEach } from 'vitest'
import i18n from './i18n'

// Every test starts in the default language. A test that switches deliberately
// also writes localStorage, and without this reset that choice would leak into
// whatever runs next in the same worker.
beforeEach(async () => {
  await i18n.changeLanguage('en')
})

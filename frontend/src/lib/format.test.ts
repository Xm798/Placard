import { describe, expect, test } from 'vitest'
import i18n from '../i18n'
import zhCN from '../i18n/locales/zh-CN'
import { fmtDateTime, fmtExpiry } from './format'

const t = i18n.t.bind(i18n)

describe('fmtExpiry', () => {
  test('null reads as never expiring', () => {
    expect(fmtExpiry(null, t)).toBe('Never expires')
  })
  test('formats a valid ISO date', () => {
    expect(fmtExpiry('2026-01-02T03:04:05Z', t)).toMatch(/^Expires \d{4}-\d{2}-\d{2}$/)
  })
  test('invalid is empty', () => {
    expect(fmtExpiry('nope', t)).toBe('')
  })
  // Asserted against the bundle rather than a copied literal: the claim is
  // "the Chinese resources are in use", not "this exact sentence exists twice".
  test('follows the active language', async () => {
    await i18n.changeLanguage('zh-CN')
    expect(fmtExpiry(null, t)).toBe(zhCN.format.neverExpires)
  })
})

describe('fmtDateTime', () => {
  test('invalid is empty', () => {
    expect(fmtDateTime('nope')).toBe('')
  })
  test('formats a valid ISO date', () => {
    expect(fmtDateTime('2026-01-02T03:04:05Z')).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}$/)
  })
})

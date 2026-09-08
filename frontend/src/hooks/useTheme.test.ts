import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { useTheme } from './useTheme'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
})

afterEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
})

describe('useTheme', () => {
  test('reads dark from localStorage and applies .dark to <html>', () => {
    localStorage.setItem('page-theme', 'dark')
    const { result } = renderHook(() => useTheme())
    expect(result.current.dark).toBe(true)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  test('toggle flips dark and updates localStorage', () => {
    localStorage.setItem('page-theme', 'dark')
    const { result } = renderHook(() => useTheme())
    act(() => result.current.toggle())
    expect(result.current.dark).toBe(false)
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    expect(localStorage.getItem('page-theme')).toBe('light')
  })
})

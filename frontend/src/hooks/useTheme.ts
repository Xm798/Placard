import { useCallback, useEffect, useState } from 'react'

// Theme hook. Ported from internal/web/app.js:62-74.
// Initial dark = localStorage 'page-theme' when present (=== 'dark'),
// else prefers-color-scheme. Applying dark toggles <html>.dark and persists.

function initialDark(): boolean {
  const saved = localStorage.getItem('page-theme')
  return saved ? saved === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches
}

export function useTheme(): { dark: boolean; toggle: () => void } {
  const [dark, setDark] = useState<boolean>(initialDark)

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    localStorage.setItem('page-theme', dark ? 'dark' : 'light')
  }, [dark])

  const toggle = useCallback(() => setDark((d) => !d), [])

  return { dark, toggle }
}

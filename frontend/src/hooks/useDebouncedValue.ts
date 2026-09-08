import { useEffect, useState } from 'react'

// Generic debounce for controlled inputs (people-search box). No prior
// debounce hook in the repo — this is the first (ShareDialog's search input).
export function useDebouncedValue<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value)

  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
  }, [value, delayMs])

  return debounced
}

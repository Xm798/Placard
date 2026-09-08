import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'

// Toast. Ported from internal/web/app.js:470-478 (2200ms auto-hide, single
// active message). React escapes textContent by default, so message injection
// is safe. Structure mirrors app.html:448-452.

interface ToastState {
  show: (msg: string) => void
  message: string
  visible: boolean
}

const ToastContext = createContext<ToastState | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [message, setMessage] = useState('')
  const [visible, setVisible] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const show = useCallback((msg: string) => {
    setMessage(msg)
    setVisible(true)
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setVisible(false), 2200)
  }, [])

  return (
    <ToastContext.Provider value={{ show, message, visible }}>{children}</ToastContext.Provider>
  )
}

export function useToast(): { show: (msg: string) => void } {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used within a ToastProvider')
  return { show: ctx.show }
}

export function ToastViewport() {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('ToastViewport must be used within a ToastProvider')
  return (
    <div
      role="status"
      aria-live="polite"
      className={
        'toast fixed bottom-6 left-1/2 -translate-x-1/2 z-50 rounded-full border border-line-strong surface px-5 py-2.5 text-sm shadow-xl flex items-center gap-2' +
        (ctx.visible ? ' show' : '')
      }
    >
      <svg
        className="w-4 h-4"
        style={{ color: '#10b981' }}
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        strokeWidth="2.5"
      >
        <path d="M5 13l4 4L19 7" />
      </svg>
      <span>{ctx.message}</span>
    </div>
  )
}

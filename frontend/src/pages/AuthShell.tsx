import type { ReactNode } from 'react'
import { LogoMark, Wordmark } from '../components/Brand'

// The frame the sign-in and registration pages share. They render outside the
// app layout: <Header/> loads /api/me, which has no answer for a visitor who is
// not signed in yet.
export default function AuthShell({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string
  subtitle: string
  children: ReactNode
  footer: ReactNode
}) {
  return (
    <main className="min-h-screen grid lg:grid-cols-2">
      <div className="auth-poster hidden lg:flex flex-col justify-between p-12" aria-hidden="true">
        <LogoMark className="logo-mark-plain w-14 h-14 text-lg" />
        <div className="font-display uppercase leading-[0.85] text-[7.5rem]">
          Pla
          <br />
          card<span className="text-accent">.</span>
        </div>
        <div className="flex gap-3">
          <span className="sticker px-3 py-1 text-sm">HTML</span>
          <span className="sticker sticker-pop px-3 py-1 text-sm">
            →
          </span>
          <span className="sticker sticker-plain px-3 py-1 text-sm">
            URL
          </span>
        </div>
      </div>
      <div className="flex items-center justify-center px-5 py-12">
        <section className="w-full max-w-sm">
          <div className="flex items-center gap-3 mb-8 lg:hidden">
            <LogoMark />
            <Wordmark />
          </div>
          <div className="card p-7">
            <h1 className="font-display text-3xl mb-2">{title}</h1>
            <p className="text-soft text-sm leading-relaxed mb-7">{subtitle}</p>
            {children}
          </div>
          <div className="mt-6 text-sm font-bold">{footer}</div>
        </section>
      </div>
    </main>
  )
}

// Field is the label + input pair both forms are built from.
export function Field({
  id,
  label,
  hint,
  ...props
}: { id: string; label: string; hint?: string } & React.ComponentProps<'input'>) {
  return (
    <div className="mb-4">
      <label htmlFor={id} className="block text-sm font-medium mb-1.5">
        {label}
      </label>
      <input
        id={id}
        className="w-full h-10 px-3 border-2 border-line-strong surface outline-none"
        {...props}
      />
      {hint ? <p className="text-faint text-xs mt-1.5">{hint}</p> : null}
    </div>
  )
}

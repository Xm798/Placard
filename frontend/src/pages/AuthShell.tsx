import type { ReactNode } from 'react'

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
    <main className="min-h-screen flex items-center justify-center px-5 py-12">
      <section className="w-full max-w-sm">
        <div className="flex items-center gap-2.5 mb-10">
          <div
            className="w-8 h-8 rounded-lg flex items-center justify-center mono text-sm font-medium"
            aria-hidden="true"
            style={{ background: 'var(--ink)', color: 'var(--ink-fg)' }}
          >
            &lt;/&gt;
          </div>
          <span className="font-display font-extrabold text-xl tracking-tight">Placard</span>
        </div>
        <h1 className="font-display font-extrabold text-3xl tracking-[-0.03em] mb-2">{title}</h1>
        <p className="text-soft text-sm leading-relaxed mb-8">{subtitle}</p>
        {children}
        <div className="mt-6 text-sm text-soft">{footer}</div>
      </section>
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
        className="w-full h-10 px-3 rounded-lg border border-line-strong bg-transparent outline-none focus:border-[var(--fg)] transition-colors"
        {...props}
      />
      {hint ? <p className="text-faint text-xs mt-1.5">{hint}</p> : null}
    </div>
  )
}

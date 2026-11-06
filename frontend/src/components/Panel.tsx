import type { ReactNode } from 'react'

export type PanelTone = 'pop' | 'accent' | 'sun'

// A settings-style section: a numbered, colour-coded label band on top of a
// card. The band carries the heading and the section's primary action so the
// body stays free for the content itself.
export default function Panel({
  id,
  index,
  title,
  titleId,
  tone = 'pop',
  action,
  children,
}: {
  id?: string
  index: number
  title: ReactNode
  titleId?: string
  tone?: PanelTone
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section id={id} className="card scroll-mt-8">
      <div className="panel-band flex items-center gap-4 px-5 py-3" data-tone={tone}>
        <span className="panel-index mono text-xs font-bold px-1.5 py-0.5" aria-hidden="true">
          {String(index).padStart(2, '0')}
        </span>
        <h2 className="font-display text-lg uppercase flex-1 min-w-0" id={titleId}>
          {title}
        </h2>
        {action}
      </div>
      {children}
    </section>
  )
}

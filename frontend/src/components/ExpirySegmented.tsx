import { useRef } from 'react'

// Segmented radiogroup with roving arrow-key focus. Ported 1:1 from
// internal/web/app.html:246-254 + app.js:143-162. Active option paints
// bg var(--ink)/color var(--ink-fg); inactive transparent/var(--fg-soft).
// Generalized so the publish retention control, the token lifetime control and
// the visibility picker share one implementation (only the options /
// labelledby / button class differ).

interface Option {
  val: string
  label: string
}

interface ExpirySegmentedProps {
  options: Option[]
  value: string
  onChange: (v: string) => void
  ariaLabelledBy: string
  btnClassName?: string
}

export default function ExpirySegmented({
  options,
  value,
  onChange,
  ariaLabelledBy,
  btnClassName = 'expiry-btn px-3 py-1.5 font-medium',
}: ExpirySegmentedProps) {
  const btns = useRef<(HTMLButtonElement | null)[]>([])

  function onKeyDown(e: React.KeyboardEvent, i: number) {
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
      e.preventDefault()
      const n = (i + 1) % options.length
      onChange(options[n].val)
      btns.current[n]?.focus()
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      e.preventDefault()
      const n = (i - 1 + options.length) % options.length
      onChange(options[n].val)
      btns.current[n]?.focus()
    }
  }

  return (
    <div
      className="inline-flex rounded-lg border border-line-strong overflow-hidden"
      role="radiogroup"
      aria-labelledby={ariaLabelledBy}
    >
      {options.map((o, i) => {
        const on = o.val === value
        return (
          <button
            key={o.val}
            type="button"
            ref={(el) => {
              btns.current[i] = el
            }}
            className={btnClassName}
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            data-val={o.val}
            style={{
              background: on ? 'var(--ink)' : 'transparent',
              color: on ? 'var(--ink-fg)' : 'var(--fg-soft)',
            }}
            onClick={() => onChange(o.val)}
            onKeyDown={(e) => onKeyDown(e, i)}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

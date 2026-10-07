import { useEffect, useId, useRef, useState } from 'react'

const natural = (a: string, b: string) => a.localeCompare(b, 'en-GB', { numeric: true })

/** "4", "4 and 6", "4, 6 and 9". */
export const platformList = (ps: string[]) =>
  ps.length < 2 ? ps.join('') : `${ps.slice(0, -1).join(', ')} and ${ps[ps.length - 1]}`

/**
 * Pick any number of platforms. A button shows the choice; it opens a panel of
 * toggles, one per platform on the board, each a real checkbox underneath.
 * Escape or a click outside closes it.
 */
export function PlatformFilter({
  platforms,
  selected,
  onChange,
}: {
  platforms: string[]
  selected: string[]
  onChange: (next: string[]) => void
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const id = useId()

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [open])

  const toggle = (p: string) =>
    onChange((selected.includes(p) ? selected.filter(x => x !== p) : [...selected, p]).sort(natural))

  const value =
    selected.length === 0
      ? 'All platforms'
      : selected.length <= 3
        ? `${selected.length === 1 ? 'Platform' : 'Platforms'} ${selected.join(', ')}`
        : `${selected.length} platforms`

  return (
    <div
      className="field multi"
      ref={box}
      onKeyDown={e => {
        if (e.key === 'Escape' && open) {
          setOpen(false)
          button.current?.focus()
        }
      }}
    >
      <span className="field-label" id={`${id}-label`}>
        Platform
      </span>
      <button
        ref={button}
        type="button"
        className={`multi-button${selected.length ? ' is-set' : ''}`}
        aria-expanded={open}
        aria-controls={`${id}-panel`}
        aria-labelledby={`${id}-label ${id}-value`}
        onClick={() => setOpen(o => !o)}
      >
        <span id={`${id}-value`}>{value}</span>
      </button>
      {open && (
        <div className="multi-panel" id={`${id}-panel`} role="group" aria-labelledby={`${id}-label`}>
          <div className="multi-head">
            <span>{selected.length ? `${selected.length} selected` : 'Showing all platforms'}</span>
            <button type="button" className="link-button" disabled={!selected.length} onClick={() => onChange([])}>
              Show all
            </button>
          </div>
          {platforms.length === 0 ? (
            <p className="multi-empty">No platforms on the board yet.</p>
          ) : (
            <div className="multi-grid">
              {platforms.map(p => (
                <label key={p} className="multi-option">
                  <input type="checkbox" checked={selected.includes(p)} onChange={() => toggle(p)} />
                  <span>
                    <span className="sr-only">Platform </span>
                    {p}
                  </span>
                </label>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

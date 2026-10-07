import { useEffect, useRef, useState } from 'react'

const DIGITS = '0123456789'
const LETTERS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'
const STEP_MS = 150

/** A stand-in character from the same family, shown mid-flip. */
function between(target: string) {
  const set = /\d/.test(target) ? DIGITS : /[a-z]/i.test(target) ? LETTERS : ''
  return set ? set[Math.floor(Math.random() * set.length)] : target
}

export function useReducedMotion() {
  const query = '(prefers-reduced-motion: reduce)'
  const [reduced, setReduced] = useState(() => window.matchMedia(query).matches)
  useEffect(() => {
    const mq = window.matchMedia(query)
    const on = () => setReduced(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return reduced
}

interface Cell {
  /** What the cell shows now. */
  c: string
  /** What it showed before the last flip, while that flip runs. */
  was?: string
  /** Counts flips, so each one restarts its animation. */
  n: number
}

const cellsOf = (s: string): Cell[] => Array.from(s, c => ({ c, n: 0 }))

/**
 * Text set in split-flap cells. When the value changes, each changed
 * character flips through a couple of others before landing, left to right,
 * so a new time or platform announces itself. On tiles the flip is a real
 * leaf: the old top half falls over the hinge, then the new bottom half drops
 * into place. With `introduce` it flips in from blank on first show. Screen
 * readers get the plain value.
 */
export function Flap({
  value,
  tiles = false,
  introduce = false,
  delay = 0,
  label,
  className,
}: {
  value: string
  tiles?: boolean
  introduce?: boolean
  delay?: number
  label?: string
  className?: string
}) {
  const reduced = useReducedMotion()
  const [cells, setCells] = useState<Cell[]>(() => cellsOf(introduce && !reduced ? ' '.repeat(value.length) : value))
  const current = useRef(cells)

  useEffect(() => {
    const from = current.current
    if (reduced) {
      current.current = cellsOf(value)
      setCells(current.current)
      return
    }
    if (from.map(x => x.c).join('') === value) return
    const work: Cell[] = Array.from(value, (_, i) => ({ c: from[i]?.c ?? ' ', n: from[i]?.n ?? 0 }))
    const timers: number[] = []
    Array.from(value).forEach((target, i) => {
      if (work[i].c === target) return
      const steps = 2 + (i % 2)
      for (let s = 1; s <= steps; s++) {
        timers.push(
          window.setTimeout(
            () => {
              const next = s === steps ? target : between(target)
              work[i] = { c: next, was: work[i].c, n: work[i].n + 1 }
              current.current = work.slice()
              setCells(current.current)
            },
            delay + i * 50 + s * STEP_MS,
          ),
        )
      }
    })
    return () => timers.forEach(t => window.clearTimeout(t))
  }, [value, reduced, delay])

  const glyph = (c: string) => (c === ' ' ? ' ' : c)
  return (
    <span className={`flap${tiles ? ' flap-tiles' : ''}${className ? ` ${className}` : ''}`}>
      <span className="sr-only">{label ?? value}</span>
      <span className="flap-cells" aria-hidden="true">
        {cells.map(({ c, was, n }, i) => (
          <span key={i} className={`flap-cell${/[^\p{L}\p{N} ]/u.test(c) ? ' flap-sep' : ''}${c === ' ' ? ' flap-blank' : ''}`}>
            {tiles ? (
              <>
                <span className="flap-ch">{glyph(c)}</span>
                {n > 0 && was !== undefined && (
                  <>
                    <span key={`u${n}`} className="leaf leaf-under">
                      {glyph(was)}
                    </span>
                    <span key={`t${n}`} className="leaf leaf-top">
                      {glyph(was)}
                    </span>
                    <span key={`b${n}`} className="leaf leaf-bottom">
                      {glyph(c)}
                    </span>
                  </>
                )}
              </>
            ) : (
              <span key={n} className={`flap-ch${n > 0 ? ' flap-turn' : ''}`}>
                {glyph(c)}
              </span>
            )}
          </span>
        ))}
      </span>
    </span>
  )
}

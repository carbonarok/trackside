import type { Stop, Times } from './api'

const timeFmt = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'Europe/London' })
const dateFmt = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'Europe/London' })

export const hhmm = (iso?: string) => (iso ? timeFmt.format(new Date(iso)) : '')
export const longDate = (isoDate: string) => dateFmt.format(new Date(`${isoDate}T12:00:00Z`))

/** Today's date in the UK as YYYY-MM-DD. */
export const ukToday = () =>
  new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/London', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date())

/**
 * Signal aspects stand for punctuality throughout: green on time, yellow a
 * little late, double yellow late, red very late or cancelled, unlit when
 * nothing has been reported.
 */
export type Aspect = 'green' | 'yellow' | 'double' | 'red' | 'unlit'

export function aspectFor(delayMinutes: number | undefined, opts: { cancelled?: boolean; delayed?: boolean; live?: boolean } = {}): Aspect {
  if (opts.cancelled) return 'red'
  if (opts.delayed) return 'double'
  if (delayMinutes === undefined || opts.live === false) return 'unlit'
  if (delayMinutes <= 0) return 'green'
  if (delayMinutes < 5) return 'yellow'
  if (delayMinutes < 15) return 'double'
  return 'red'
}

export const aspectLabel: Record<Aspect, string> = {
  green: 'On time',
  yellow: '1 to 4 minutes late',
  double: '5 to 14 minutes late',
  red: '15 minutes or more late',
  unlit: 'Nothing reported yet',
}

export interface Status {
  text: string
  detail?: string
  aspect: Aspect
}

/** What to say about an arrival or departure on a board. */
export function eventStatus(stop: Stop, t: Times | undefined, arriving: boolean): Status {
  if (stop.cancelled || t?.cancelled) return { text: 'Cancelled', aspect: 'red' }
  if (!t) return { text: '', aspect: 'unlit' }
  const late = t.delayMinutes
  if (t.actual) {
    return {
      text: `${arriving ? 'Arrived' : 'Departed'} ${hhmm(t.actual)}`,
      detail: lateness(late),
      aspect: aspectFor(late),
    }
  }
  if (stop.atPlatform) return { text: 'At platform', detail: lateness(late), aspect: aspectFor(late ?? 0) }
  if (t.delayed) return { text: 'Delayed', aspect: 'double' }
  if (t.estimated) {
    if (late !== undefined && late > 0) return { text: `Expected ${hhmm(t.estimated)}`, detail: lateness(late), aspect: aspectFor(late) }
    return { text: stop.approaching ? 'Approaching' : 'On time', aspect: 'green' }
  }
  return { text: 'No report yet', aspect: 'unlit' }
}

export function lateness(minutes: number | undefined): string | undefined {
  if (minutes === undefined || minutes === 0) return undefined
  if (minutes < 0) return `${-minutes} min early`
  return `${minutes} min late`
}

/** The booked time shown for a stop: public time, else working time. */
export const bookedTime = (t?: Times) => hhmm(t?.public ?? t?.working)

export const platformOf = (stop: Stop) => stop.platform?.actual || stop.platform?.planned

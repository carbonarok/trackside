// Types and fetchers for the trackside API (see /openapi.yaml).

export interface Location {
  tiploc: string
  crs?: string
  name: string
}

export interface Operator {
  code: string
  name: string
}

export interface Endpoint extends Location {
  time?: string
}

export interface Times {
  public?: string
  working?: string
  actual?: string
  estimated?: string
  delayMinutes?: number
  delayed?: boolean
  cancelled?: boolean
}

export interface Platform {
  planned?: string
  actual?: string
  changed: boolean
  confirmed: boolean
  /** National Rail hasn't announced it yet; only the booked platform is given. */
  suppressed?: boolean
}

export interface Stop extends Location {
  kind: 'origin' | 'call' | 'pass' | 'destination' | 'stop'
  arrival?: Times
  departure?: Times
  pass?: Times
  platform?: Platform
  cancelled: boolean
  startsHere?: boolean
  atPlatform?: boolean
  approaching?: boolean
  terminatesHere?: boolean
}

export interface ServiceSummary {
  uid: string
  runDate: string
  headcode?: string
  operator?: Operator
  isPassenger: boolean
  status: 'scheduled' | 'activated' | 'running' | 'terminated' | 'cancelled' | 'partially_cancelled'
  origin: Endpoint[]
  destination: Endpoint[]
  cancelReasonCode?: string
  cancelReasonCodeDescription?: string
  cancelReason?: string
  lateReason?: string
  plannedCancel: boolean
}

export interface BoardService extends ServiceSummary {
  stop: Stop
}

export interface Message {
  id: number
  category: string
  severity: number
  text: string
  html: string
  updatedAt: string
}

export interface Board {
  location: Location
  from: string
  to: string
  messages: Message[]
  services: BoardService[]
}

export interface Association {
  type: 'divides' | 'divided_from' | 'joined_by' | 'joins' | 'forms' | 'formed_from' | 'linked' | 'associated'
  category: string
  location: Location
  cancelled: boolean
  service: ServiceSummary
}

export interface ServiceDetail extends ServiceSummary {
  trainClass?: string
  powerType?: string
  category?: string
  source: 'timetable' | 'vstp'
  position?: { tdArea: string; berth: string; at: string }
  associations?: Association[]
  stops: Stop[]
}

export interface TrainPosition {
  uid: string
  runDate: string
  headcode?: string
  operator?: Operator
  origin: string
  destination: string
  last?: Location
  next?: Location
  atStation: boolean
  bearing: number
  delayMinutes?: number
  live: boolean
  isPassenger: boolean
}

export interface TrainMap {
  type: 'FeatureCollection'
  generatedAt: string
  features: { type: 'Feature'; geometry: { type: 'Point'; coordinates: [number, number] }; properties: TrainPosition }[]
}

export interface StationMap {
  type: 'FeatureCollection'
  features: { type: 'Feature'; geometry: { type: 'Point'; coordinates: [number, number] }; properties: Location }[]
}

export interface TrainRef {
  uid: string
  runDate: string
  headcode?: string
  operator?: Operator
}

export interface DelayRepay {
  from: Location
  to: Location
  bookedDeparture: string
  bookedArrival: string
  train: TrainRef
  cancelled: boolean
  cancelReason?: string
  lateReason?: string
  usedTrain?: TrainRef
  actualDeparture?: string
  arrivedAt?: string
  arrivalSource?: string
  delayMinutes?: number
  eligible: boolean
  band?: string
  compensation?: { singlePercent: number; returnPercent: number }
  note: string
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: 'application/json' } })
  if (!res.ok) {
    let message = `Request failed (${res.status})`
    try {
      const body = await res.json()
      if (body.error) message = body.error
    } catch {
      // not JSON
    }
    throw new ApiError(res.status, message)
  }
  return res.json() as Promise<T>
}

const qs = (params: Record<string, string | undefined>) => {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v) p.set(k, v)
  const s = p.toString()
  return s ? `?${s}` : ''
}

export const api = {
  searchLocations: (q: string, signal?: AbortSignal) =>
    get<{ locations: Location[] }>(`/v1/locations${qs({ q })}`, signal).then(r => r.locations),
  searchServices: (q: string, date?: string, signal?: AbortSignal) =>
    get<{ services: ServiceSummary[] }>(`/v1/services${qs({ q, date })}`, signal).then(r => r.services),
  board: (code: string, kind: 'departures' | 'arrivals', opts: { at?: string; window?: string; to?: string; from?: string }, signal?: AbortSignal) =>
    get<Board>(`/v1/locations/${encodeURIComponent(code)}/${kind}${qs(opts)}`, signal),
  service: (uid: string, date: string, signal?: AbortSignal) =>
    get<ServiceDetail>(`/v1/services/${encodeURIComponent(uid)}/${date}`, signal),
  trains: (all: boolean, signal?: AbortSignal) => get<TrainMap>(`/v1/map/trains${all ? '?all=true' : ''}`, signal),
  stations: (signal?: AbortSignal) => get<StationMap>('/v1/map/stations', signal),
  delayRepay: (p: { from: string; to: string; date: string; departure: string }, signal?: AbortSignal) =>
    get<DelayRepay>(`/v1/delay-repay${qs(p)}`, signal),
}

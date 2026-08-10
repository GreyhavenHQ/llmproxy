// Range presets and UTC bucket maths for the usage views. Buckets are UTC,
// matching how the proxy records events.
//
// The usage overview carries its own copy of this logic; this module is the
// shared home for anything written since. Fold the overview in when it next
// needs a change.

export type Bucket = 'hour' | 'day' | 'week' | 'month'

export interface Range {
  key: string
  label: string
  bucket: Bucket
  count: number // 0 = everything recorded, no previous window to compare against
}

export const RANGES: Range[] = [
  { key: '24h', label: 'Last 24 hours', bucket: 'hour', count: 24 },
  { key: '7d', label: 'Last 7 days', bucket: 'day', count: 7 },
  { key: '30d', label: 'Last 30 days', bucket: 'day', count: 30 },
  { key: '12w', label: 'Last 12 weeks', bucket: 'week', count: 12 },
  { key: '12mo', label: 'Last 12 months', bucket: 'month', count: 12 },
  { key: 'all', label: 'All time', bucket: 'month', count: 0 },
]

export function floorBucket(date: Date, bucket: Bucket): Date {
  const d = new Date(date)
  d.setUTCMilliseconds(0)
  d.setUTCSeconds(0)
  d.setUTCMinutes(0)
  if (bucket === 'hour') return d
  d.setUTCHours(0)
  if (bucket === 'month') {
    d.setUTCDate(1)
    return d
  }
  if (bucket === 'week') {
    d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7)) // back to Monday
  }
  return d
}

export function addBuckets(date: Date, bucket: Bucket, n: number): Date {
  const d = new Date(date)
  if (bucket === 'hour') d.setUTCHours(d.getUTCHours() + n)
  else if (bucket === 'day') d.setUTCDate(d.getUTCDate() + n)
  else if (bucket === 'week') d.setUTCDate(d.getUTCDate() + n * 7)
  else d.setUTCMonth(d.getUTCMonth() + n)
  return d
}

const UTC: Intl.DateTimeFormatOptions = { timeZone: 'UTC' }

export function bucketLabel(start: string, bucket: Bucket): string {
  const d = new Date(start)
  if (bucket === 'hour') {
    return d.toLocaleTimeString(undefined, { ...UTC, hour: '2-digit', minute: '2-digit' })
  }
  if (bucket === 'month') {
    return d.toLocaleDateString(undefined, { ...UTC, month: 'short', year: '2-digit' })
  }
  return d.toLocaleDateString(undefined, { ...UTC, month: 'short', day: 'numeric' })
}

export function bucketTitle(start: string, bucket: Bucket): string {
  const d = new Date(start)
  switch (bucket) {
    case 'hour':
      return `${d.toLocaleDateString(undefined, { ...UTC, month: 'short', day: 'numeric' })}, ${d.toLocaleTimeString(undefined, { ...UTC, hour: '2-digit', minute: '2-digit' })} UTC`
    case 'week':
      return `Week of ${d.toLocaleDateString(undefined, { ...UTC, month: 'short', day: 'numeric', year: 'numeric' })}`
    case 'month':
      return d.toLocaleDateString(undefined, { ...UTC, month: 'long', year: 'numeric' })
    default:
      return d.toLocaleDateString(undefined, {
        ...UTC,
        month: 'short',
        day: 'numeric',
        year: 'numeric',
      })
  }
}

// Small hand-rolled SVG charts. The design system ships chart tokens
// (--chart-1 accent, --chart-2..5 grays), so series identity here is one
// accent hue plus a gray step, backed by a legend, direct labels on the
// extreme, and the table view under every chart.

import { useLayoutEffect, useRef, useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface ChartSeries {
  name: string
  fill: string // SVG fill class for the mark
  swatch: string // matching background class for the legend key
}

export interface ChartPoint {
  label: string // x-axis tick
  title: string // tooltip heading
  values: (number | null)[] // one per series; null renders no mark
  rows?: { label: string; value: string }[] // extra tooltip lines
}

// The series slots. Slot 2 takes a different gray per mode because the
// design system's dark ramp runs the other way; both clear contrast and
// colour-vision separation against their surface. The mid slot is the third
// segment of a token stack (cached input), the mid gray sitting a clear
// lightness step from slot 2 in both modes.
export const SERIES_ACCENT: ChartSeries = {
  name: '',
  fill: 'fill-chart-1',
  swatch: 'bg-chart-1',
}
export const SERIES_GRAY: ChartSeries = {
  name: '',
  fill: 'fill-chart-2 dark:fill-chart-4',
  swatch: 'bg-chart-2 dark:bg-chart-4',
}
export const SERIES_GRAY_MID: ChartSeries = {
  name: '',
  fill: 'fill-chart-3',
  swatch: 'bg-chart-3',
}

const GUTTER = 52 // room for y-axis labels
const AXIS = 22 // x-axis label band
const PLOT = 176
const BAR_MAX = 24
const GAP = 2 // the surface gap between stacked segments

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [width, setWidth] = useState(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    setWidth(el.getBoundingClientRect().width)
    const observer = new ResizeObserver((entries) => {
      setWidth(entries[0].contentRect.width)
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
  return [ref, width] as const
}

// niceTicks rounds the scale to human numbers (0 / 500 / 1,000 / 1,500). The
// top tick always sits above the peak, so the tallest bar keeps room for its
// direct label.
function niceTicks(max: number, integer: boolean, count = 4): number[] {
  if (!(max > 0)) return [0, 1]
  const magnitude = Math.pow(10, Math.floor(Math.log10(max / count)))
  const normalised = max / count / magnitude
  let step =
    (normalised <= 1 ? 1 : normalised <= 2 ? 2 : normalised <= 5 ? 5 : 10) * magnitude
  if (integer) step = Math.max(1, Math.round(step))
  const top = (Math.floor(max / step) + 1) * step
  const ticks: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(Number(v.toFixed(10)))
  return ticks
}

// Data-end rounded, square at the baseline.
function barPath(x: number, y: number, w: number, h: number, round: boolean): string {
  const r = round ? Math.min(4, w / 2, h) : 0
  return `M${x},${y + h} L${x},${y + r} Q${x},${y} ${x + r},${y} L${x + w - r},${y} Q${x + w},${y} ${x + w},${y + r} L${x + w},${y + h} Z`
}

export function ColumnChart({
  points,
  series,
  format,
  integer,
  emptyMessage = 'No usage in this range.',
  className,
}: {
  points: ChartPoint[]
  series: ChartSeries[]
  format: (n: number) => string
  /** Counts have no half: keep the axis on whole numbers. */
  integer?: boolean
  emptyMessage?: string
  className?: string
}) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const [hovered, setHovered] = useState<number | null>(null)

  const totals = points.map((p) =>
    p.values.reduce((sum: number, v) => sum + (v ?? 0), 0),
  )
  const peak = Math.max(0, ...totals)
  const ticks = niceTicks(peak, integer === true)
  const top = ticks[ticks.length - 1] || 1
  const plotW = Math.max(0, width - GUTTER)
  const band = points.length > 0 ? plotW / points.length : 0
  const barW = Math.max(2, Math.min(BAR_MAX, band - 6))
  const y = (value: number) => PLOT - (value / top) * PLOT
  const peakIndex = totals.indexOf(peak)
  // Show at most eight x labels; past that they collide.
  const labelEvery = Math.max(1, Math.ceil(points.length / 8))

  return (
    <div className={cn('flex flex-col gap-2', className)}>
      {series.length > 1 && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          {series.map((s) => (
            <span key={s.name} className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className={cn('size-2.5 rounded-xs', s.swatch)} aria-hidden />
              {s.name}
            </span>
          ))}
        </div>
      )}
      <div ref={ref} className="relative w-full">
        <svg
          width={width}
          height={PLOT + AXIS}
          className="block overflow-visible"
          onPointerLeave={() => setHovered(null)}
        >
          {ticks.map((tick) => (
            <g key={tick}>
              <line
                x1={GUTTER}
                x2={width}
                y1={y(tick)}
                y2={y(tick)}
                className="stroke-border"
                strokeWidth={1}
              />
              <text
                x={GUTTER - 8}
                y={y(tick) + 3}
                textAnchor="end"
                className="fill-muted-foreground text-[10px] tabular-nums"
              >
                {format(tick)}
              </text>
            </g>
          ))}

          {/* A wash behind the hovered band, so the chart responds without
              dimming every other bar. */}
          {hovered !== null && (
            <rect
              x={GUTTER + hovered * band}
              y={0}
              width={Math.max(band, 1)}
              height={PLOT}
              className="fill-foreground/5"
            />
          )}

          {points.map((point, i) => {
            const x = GUTTER + i * band + (band - barW) / 2
            // Only the topmost segment of a stack carries the rounded data-end.
            let topSegment = -1
            point.values.forEach((v, s) => {
              if (v !== null && v > 0) topSegment = s
            })
            let baseline = PLOT
            return (
              <g key={i}>
                {point.values.map((value, s) => {
                  if (value === null || value <= 0) return null
                  const full = (value / top) * PLOT
                  // The 2px surface gap sits under each stacked segment.
                  const height = Math.max(1, full - (baseline < PLOT ? GAP : 0))
                  const barY = baseline - full
                  baseline = barY
                  return (
                    <path
                      key={s}
                      d={barPath(x, barY, barW, height, s === topSegment)}
                      className={series[s].fill}
                    />
                  )
                })}
              </g>
            )
          })}

          {/* Direct-label the extreme; the axis and tooltips carry the rest. */}
          {peak > 0 && band >= 30 && (
            <text
              x={GUTTER + peakIndex * band + band / 2}
              y={y(peak) - 6}
              textAnchor="middle"
              className="fill-foreground text-[10px] font-medium tabular-nums"
            >
              {format(peak)}
            </text>
          )}

          {points.map((point, i) =>
            i % labelEvery === 0 ? (
              <text
                key={i}
                x={GUTTER + i * band + band / 2}
                y={PLOT + 15}
                textAnchor="middle"
                className="fill-muted-foreground text-[10px]"
              >
                {point.label}
              </text>
            ) : null,
          )}

          <line
            x1={GUTTER}
            x2={width}
            y1={PLOT}
            y2={PLOT}
            className="stroke-border"
            strokeWidth={1}
          />

          {/* Hit targets: the whole band, so nobody has to aim at a thin bar. */}
          {points.map((point, i) => (
            <rect
              key={i}
              x={GUTTER + i * band}
              y={0}
              width={Math.max(band, 1)}
              height={PLOT}
              fill="transparent"
              tabIndex={0}
              role="button"
              aria-label={`${point.title}: ${point.values
                .map((v, s) => `${series[s].name || 'value'} ${v === null ? 'unpriced' : format(v)}`)
                .join(', ')}`}
              onPointerEnter={() => setHovered(i)}
              onFocus={() => setHovered(i)}
              onBlur={() => setHovered(null)}
              className="outline-none focus-visible:stroke-ring focus-visible:[stroke-width:2]"
            />
          ))}
        </svg>

        {peak === 0 && (
          <p className="pointer-events-none absolute inset-x-0 top-1/2 -translate-y-1/2 text-center text-sm text-muted-foreground">
            {emptyMessage}
          </p>
        )}

        {hovered !== null && points[hovered] && (
          <div
            className="pointer-events-none absolute z-10 min-w-36 -translate-x-1/2 rounded-md border bg-popover p-2 text-xs shadow-md"
            style={{
              left: Math.min(
                Math.max(GUTTER + hovered * band + band / 2, GUTTER + 70),
                Math.max(width - 70, GUTTER + 70),
              ),
              top: 0,
            }}
          >
            <p className="mb-1 text-muted-foreground">{points[hovered].title}</p>
            {points[hovered].values.map((value, s) => (
              <p key={s} className="flex items-center gap-1.5">
                <span className={cn('h-0.5 w-3 rounded-full', series[s].swatch)} aria-hidden />
                <span className="font-medium tabular-nums">
                  {value === null ? 'unpriced' : format(value)}
                </span>
                {series[s].name && (
                  <span className="text-muted-foreground">{series[s].name}</span>
                )}
              </p>
            ))}
            {points[hovered].rows?.map((row) => (
              <p key={row.label} className="flex items-center gap-1.5 pl-[18px]">
                <span className="font-medium tabular-nums">{row.value}</span>
                <span className="text-muted-foreground">{row.label}</span>
              </p>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ---------- donut ----------

export interface DonutSlice {
  label: string
  value: number
  /** Extra tooltip lines for this slice. */
  rows?: { label: string; value: string }[]
}

// The slice ramp: accent for the largest share, then a gray lightness ramp
// stepped wide enough that adjacent slices stay apart (validated ΔE ≥ 22 in
// both modes). Identity is carried by the labelled list, not by hue: the
// design system is one accent plus grays on purpose.
const DONUT_FILLS = [
  { fill: 'fill-chart-1', swatch: 'bg-chart-1' },
  { fill: 'fill-chart-5', swatch: 'bg-chart-5' },
  { fill: 'fill-chart-3', swatch: 'bg-chart-3' },
  { fill: 'fill-border', swatch: 'bg-border' },
]
/** Donut shows at most this many slices; the tail folds into "Other". */
export const DONUT_MAX_SLICES = DONUT_FILLS.length

// foldDonut caps the slice count: up to the budget everything shows as-is,
// past it the tail folds into one aggregate slice (built by the caller, which
// knows how to merge its entities) and rides along as overflow for the
// component to unfold on demand.
export function foldDonut<T>(
  items: T[],
  toSlice: (item: T) => DonutSlice,
  fold: (tail: T[]) => DonutSlice,
): { slices: DonutSlice[]; overflow: DonutSlice[] } {
  if (items.length <= DONUT_MAX_SLICES) {
    return { slices: items.map(toSlice), overflow: [] }
  }
  const tail = items.slice(DONUT_MAX_SLICES - 1)
  return {
    slices: [...items.slice(0, DONUT_MAX_SLICES - 1).map(toSlice), fold(tail)],
    overflow: tail.map(toSlice),
  }
}

function arcPath(cx: number, cy: number, r0: number, r1: number, a0: number, a1: number): string {
  const p = (r: number, a: number) => `${cx + r * Math.sin(a)},${cy - r * Math.cos(a)}`
  // A slice spanning the whole circle starts and ends on the same point,
  // which SVG draws as nothing. Draw the full annulus instead: two circles
  // wound in opposite directions so the nonzero fill rule keeps the hole.
  if (a1 - a0 >= Math.PI * 2 - 1e-9) {
    const ring = (r: number, sweep: 0 | 1) =>
      `M${cx},${cy - r} A${r},${r} 0 1 ${sweep} ${cx},${cy + r} A${r},${r} 0 1 ${sweep} ${cx},${cy - r} Z`
    return `${ring(r0, 1)} ${ring(r1, 0)}`
  }
  const large = a1 - a0 > Math.PI ? 1 : 0
  return (
    `M${p(r0, a0)} A${r0},${r0} 0 ${large} 1 ${p(r0, a1)} ` +
    `L${p(r1, a1)} A${r1},${r1} 0 ${large} 0 ${p(r1, a0)} Z`
  )
}

/** Expanding "Other" reveals its members in pages of this size. */
const DONUT_OVERFLOW_PAGE = 20

// Donut: share of a whole across a handful of entities. Every slice is
// direct-labelled in the list beside it (name, share, value), so color never
// carries identity alone. Callers fold the tail into "Other" beforehand and
// pass the folded entities as overflow: clicking "Other" (slice or row)
// unfolds them into the list, a page at a time.
export function Donut({
  slices,
  overflow = [],
  format,
  emptyMessage = 'No usage in this range.',
}: {
  slices: DonutSlice[]
  /** The entities aggregated into the last slice, largest first. */
  overflow?: DonutSlice[]
  format: (n: number) => string
  emptyMessage?: string
}) {
  const [hovered, setHovered] = useState<number | null>(null)
  // 0 = collapsed; otherwise how many overflow rows are unfolded.
  const [shown, setShown] = useState(0)
  const otherIndex = overflow.length > 0 ? slices.length - 1 : -1
  const toggleOther = () =>
    setShown((n) => (n === 0 ? Math.min(DONUT_OVERFLOW_PAGE, overflow.length) : 0))
  const total = slices.reduce((sum, s) => sum + s.value, 0)
  const SIZE = 168
  const R1 = 82
  const R0 = 50
  const cx = SIZE / 2
  const cy = SIZE / 2

  if (total <= 0) {
    return <p className="py-8 text-center text-sm text-muted-foreground">{emptyMessage}</p>
  }

  // The 2px surface gap between slices, expressed as an angle at mid-radius;
  // skipped when a single slice is the whole donut.
  const gap = slices.length > 1 ? 2 / ((R0 + R1) / 2) : 0
  let angle = 0
  const arcs = slices.map((slice, i) => {
    const sweep = (slice.value / total) * Math.PI * 2
    const a0 = angle + gap / 2
    const a1 = Math.max(a0, angle + sweep - gap / 2)
    angle += sweep
    return { slice, a0, a1, i }
  })

  return (
    <div className="flex flex-wrap items-center gap-x-8 gap-y-4">
      <div className="relative shrink-0">
        <svg width={SIZE} height={SIZE} className="block" onPointerLeave={() => setHovered(null)}>
          {arcs.map(({ slice, a0, a1, i }) => (
            <path
              key={i}
              d={arcPath(cx, cy, R1, R0, a0, a1)}
              className={cn(
                DONUT_FILLS[i % DONUT_FILLS.length].fill,
                'outline-none transition-opacity focus-visible:stroke-ring focus-visible:[stroke-width:2]',
                hovered !== null && hovered !== i && 'opacity-50',
                i === otherIndex && 'cursor-pointer',
              )}
              tabIndex={0}
              role={i === otherIndex ? 'button' : 'img'}
              aria-label={
                `${slice.label}: ${format(slice.value)} (${Math.round((slice.value / total) * 100)}%)` +
                (i === otherIndex ? (shown === 0 ? '; activate to expand' : '; activate to collapse') : '')
              }
              onClick={i === otherIndex ? toggleOther : undefined}
              onKeyDown={
                i === otherIndex
                  ? (e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault()
                        toggleOther()
                      }
                    }
                  : undefined
              }
              onPointerEnter={() => setHovered(i)}
              onFocus={() => setHovered(i)}
              onBlur={() => setHovered(null)}
            />
          ))}
        </svg>
        {hovered !== null && slices[hovered] && (
          <div className="pointer-events-none absolute left-1/2 top-0 z-10 min-w-32 -translate-x-1/2 rounded-md border bg-popover p-2 text-xs shadow-md">
            <p className="mb-1 text-muted-foreground">{slices[hovered].label}</p>
            <p className="font-medium tabular-nums">
              {format(slices[hovered].value)}{' '}
              <span className="font-normal text-muted-foreground">
                · {((slices[hovered].value / total) * 100).toFixed(1)}%
              </span>
            </p>
            {slices[hovered].rows?.map((row) => (
              <p key={row.label} className="flex items-center gap-1.5">
                <span className="font-medium tabular-nums">{row.value}</span>
                <span className="text-muted-foreground">{row.label}</span>
              </p>
            ))}
          </div>
        )}
      </div>
      <ul className="min-w-48 flex-1">
        {slices.map((slice, i) => (
          <li
            key={i}
            className={cn('rounded-sm text-sm', hovered === i && 'bg-foreground/5')}
            onPointerEnter={() => setHovered(i)}
            onPointerLeave={() => setHovered(null)}
          >
            {i === otherIndex ? (
              <button
                type="button"
                className="flex w-full cursor-pointer items-center gap-2 rounded-sm px-1 py-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-expanded={shown > 0}
                onClick={toggleOther}
              >
                <span
                  className={cn('size-2.5 shrink-0 rounded-xs', DONUT_FILLS[i % DONUT_FILLS.length].swatch)}
                  aria-hidden
                />
                <span className="truncate font-mono text-xs">{slice.label}</span>
                {shown > 0 ? (
                  <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                ) : (
                  <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                )}
                <span className="ml-auto tabular-nums text-muted-foreground">
                  {format(slice.value)}
                </span>
                <span className="w-12 text-right tabular-nums">
                  {((slice.value / total) * 100).toFixed(1)}%
                </span>
              </button>
            ) : (
              <span className="flex items-center gap-2 px-1 py-1">
                <span
                  className={cn('size-2.5 shrink-0 rounded-xs', DONUT_FILLS[i % DONUT_FILLS.length].swatch)}
                  aria-hidden
                />
                <span className="truncate font-mono text-xs" title={slice.label}>
                  {slice.label}
                </span>
                <span className="ml-auto tabular-nums text-muted-foreground">
                  {format(slice.value)}
                </span>
                <span className="w-12 text-right tabular-nums">
                  {((slice.value / total) * 100).toFixed(1)}%
                </span>
              </span>
            )}
          </li>
        ))}
        {overflow.slice(0, shown).map((slice, i) => (
          <li
            key={`overflow-${i}`}
            className="flex items-center gap-2 py-1 pl-[26px] pr-1 text-sm"
            onPointerEnter={() => setHovered(otherIndex)}
            onPointerLeave={() => setHovered(null)}
          >
            <span className="truncate font-mono text-xs" title={slice.label}>
              {slice.label}
            </span>
            <span className="ml-auto tabular-nums text-muted-foreground">
              {format(slice.value)}
            </span>
            <span className="w-12 text-right tabular-nums">
              {((slice.value / total) * 100).toFixed(1)}%
            </span>
          </li>
        ))}
        {shown > 0 && shown < overflow.length && (
          <li className="py-1 pl-[26px]">
            <button
              type="button"
              className="cursor-pointer text-xs text-muted-foreground underline-offset-2 outline-none hover:text-foreground hover:underline focus-visible:ring-2 focus-visible:ring-ring"
              onClick={() =>
                setShown((n) => Math.min(n + DONUT_OVERFLOW_PAGE, overflow.length))
              }
            >
              Show {Math.min(DONUT_OVERFLOW_PAGE, overflow.length - shown)} more
            </button>
          </li>
        )}
      </ul>
    </div>
  )
}

// ---------- share bars ----------

export interface ShareRow {
  label: string
  /** Full untruncated identity, shown on hover (e.g. the raw User-Agent set). */
  title?: string
  value: number
}

// ShareBars: magnitude comparison as a labelled meter list, one accent hue.
// Everything is visible in text, so there is no hover layer to need.
export function ShareBars({
  rows,
  format,
  emptyMessage = 'No usage in this range.',
}: {
  rows: ShareRow[]
  format: (n: number) => string
  emptyMessage?: string
}) {
  const peak = Math.max(0, ...rows.map((r) => r.value))
  if (rows.length === 0 || peak <= 0) {
    return <p className="py-8 text-center text-sm text-muted-foreground">{emptyMessage}</p>
  }
  const total = rows.reduce((sum, r) => sum + r.value, 0)
  return (
    <ul className="flex flex-col gap-2">
      {rows.map((row, i) => (
        <li key={i} className="grid grid-cols-[minmax(6rem,14rem)_1fr_auto] items-center gap-3 text-sm">
          <span className="truncate font-mono text-xs" title={row.title ?? row.label}>
            {row.label}
          </span>
          <span className="h-2 overflow-hidden rounded-full bg-muted" aria-hidden>
            <span
              className="block h-full rounded-full bg-chart-1"
              style={{ width: `${Math.max(1, (row.value / peak) * 100)}%` }}
            />
          </span>
          <span className="tabular-nums text-muted-foreground">
            {format(row.value)}
            <span className="ml-2 inline-block w-12 text-right text-foreground">
              {((row.value / total) * 100).toFixed(1)}%
            </span>
          </span>
        </li>
      ))}
    </ul>
  )
}

// StatTile: label, value, and an optional signed delta against the previous
// window of the same length. secondary is a quieter figure beside the value
// (e.g. the cached share of an input total).
export function StatTile({
  label,
  value,
  secondary,
  delta,
  hint,
}: {
  label: string
  value: string
  secondary?: string
  delta?: number | null
  hint?: string
}) {
  return (
    <div className="flex flex-col gap-1 rounded-lg border bg-card p-4">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-2xl font-semibold">
        {value}
        {secondary && (
          <span className="ml-2 text-sm font-normal text-muted-foreground">{secondary}</span>
        )}
      </span>
      <span className="text-xs text-muted-foreground">
        {delta === null || delta === undefined ? (
          (hint ?? ' ')
        ) : (
          <>
            {delta >= 0 ? '+' : ''}
            {Math.round(delta * 100)}% vs previous
          </>
        )}
      </span>
    </div>
  )
}

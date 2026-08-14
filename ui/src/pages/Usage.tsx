// The Usage tab and its panes. The subtab rides in the URL as a second
// segment (/usage, /usage/apps, /usage/errors), handled in App.tsx.

import { UsageDashboard } from '@/components/UsageDashboard'
import { AppsUsage } from '@/components/AppsUsage'
import { ErrorsDashboard } from '@/components/ErrorsDashboard'
import { cn } from '@/lib/utils'

const SUBS = [
  { key: 'overview', label: 'Overview' },
  { key: 'apps', label: 'Apps' },
  { key: 'errors', label: 'Errors' },
]

// A quieter control than the admin sub-nav: underlined links, one text row.
function SubNav({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <nav className="-mt-2 flex items-center gap-4 border-b text-sm">
      {SUBS.map((s) => (
        <button
          key={s.key}
          type="button"
          aria-current={value === s.key ? 'page' : undefined}
          onClick={() => onChange(s.key)}
          className={cn(
            'cursor-pointer border-b-2 px-0.5 pb-2 outline-none focus-visible:ring-2 focus-visible:ring-ring',
            value === s.key
              ? 'border-foreground font-medium text-foreground'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          {s.label}
        </button>
      ))}
    </nav>
  )
}

export function Usage({
  ssoEnabled,
  sub,
  onSubChange,
}: {
  ssoEnabled: boolean
  sub: string
  onSubChange: (v: string) => void
}) {
  return (
    <div className="flex flex-col gap-6">
      <SubNav value={sub} onChange={onSubChange} />
      {sub === 'apps' ? (
        <AppsUsage />
      ) : sub === 'errors' ? (
        <ErrorsDashboard />
      ) : (
        <UsageDashboard ssoEnabled={ssoEnabled} />
      )}
    </div>
  )
}

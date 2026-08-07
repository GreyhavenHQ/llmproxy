// The select-with-clear-button used by every filter row: the usage dashboard
// and the request explorer, so the two stay identical.

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { X } from 'lucide-react'

// A value that reads well on its own, or one whose label differs from what
// the server filters on (an API key: shown by label, filtered by id).
export type FilterOption = string | { value: string; label: string }

const ALL = 'all'

export function FilterSelect({
  label,
  value,
  onChange,
  options,
  allLabel,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: FilterOption[]
  allLabel: string
}) {
  const items = options.map((o) => (typeof o === 'string' ? { value: o, label: o } : o))
  return (
    <div className="relative">
      <Select value={value || ALL} onValueChange={(v) => onChange(v === ALL ? '' : v)}>
        <SelectTrigger className="w-44" aria-label={label}>
          {/* The value pads itself when the clear button is shown, so the
              chevron keeps its natural spot at the far right. */}
          <SelectValue className={value ? 'pr-6' : undefined} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{allLabel}</SelectItem>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {value && (
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Clear ${label.toLowerCase()} filter`}
          title={`Clear ${label.toLowerCase()} filter`}
          className="absolute top-1/2 right-8 size-6 -translate-y-1/2"
          onClick={() => onChange('')}
        >
          <X className="size-3.5" />
        </Button>
      )}
    </div>
  )
}

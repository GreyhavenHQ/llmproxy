// A text input with a filtered suggestion list. Free text is always allowed:
// discovery can fail, and a provider does not have to list a model for it to
// be servable.

import { useEffect, useMemo, useRef, useState } from 'react'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'
import { Check, ChevronDown, X } from 'lucide-react'

export interface ComboboxOption {
  value: string
  hint?: string
}

export function Combobox({
  id,
  value,
  onChange,
  options,
  loading,
  note,
  placeholder,
  className,
}: {
  id?: string
  value: string
  onChange: (value: string) => void
  options: ComboboxOption[]
  loading?: boolean
  note?: string
  placeholder?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  // The current value only filters while the user is typing. Opening the
  // list by focus, chevron or arrow key shows everything: a filled field
  // would otherwise filter down to itself and hide the alternatives.
  const [filtering, setFiltering] = useState(false)
  const wrap = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  // Sampled on pointerdown so the chevron toggles correctly even when the
  // click also focuses the input (whose focus handler opens the list).
  const openBeforeToggle = useRef(false)

  const matches = useMemo(() => {
    if (!filtering) return options.slice(0, 50)
    const needle = value.trim().toLowerCase()
    const hit = options.filter((o) => o.value.toLowerCase().includes(needle))
    return hit.slice(0, 50)
  }, [options, value, filtering])

  const openAll = () => {
    setFiltering(false)
    const all = options.slice(0, 50)
    const i = all.findIndex((o) => o.value === value)
    setActive(i >= 0 ? i : 0)
    setOpen(true)
  }

  useEffect(() => {
    const onPointerDown = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [])

  const pick = (option: ComboboxOption) => {
    onChange(option.value)
    setFiltering(false)
    setOpen(false)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      if (!open) {
        openAll()
        return
      }
      const step = e.key === 'ArrowDown' ? 1 : -1
      setActive((i) => (i + step + matches.length) % Math.max(matches.length, 1))
    } else if (e.key === 'Enter' && open && matches[active]) {
      e.preventDefault()
      pick(matches[active])
    } else if (e.key === 'Escape') {
      setOpen(false)
    }
  }

  return (
    <div ref={wrap} className={cn('relative', className)}>
      <Input
        ref={input}
        id={id}
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        autoComplete="off"
        className="pr-14 font-mono text-xs"
        placeholder={placeholder}
        value={value}
        onChange={(e) => {
          onChange(e.target.value)
          setFiltering(true)
          setActive(0)
          setOpen(true)
        }}
        onFocus={openAll}
        onKeyDown={onKeyDown}
      />
      <div className="absolute inset-y-0 right-1 flex items-center">
        {loading ? (
          <Spinner className="text-muted-foreground mr-1" />
        ) : (
          value !== '' && (
            <button
              type="button"
              aria-label="Clear"
              tabIndex={-1}
              className="text-muted-foreground hover:text-foreground rounded p-1"
              onPointerDown={(e) => e.preventDefault()}
              onClick={() => {
                onChange('')
                setFiltering(false)
                setActive(0)
                setOpen(true)
                input.current?.focus()
              }}
            >
              <X className="size-3.5" />
            </button>
          )
        )}
        <button
          type="button"
          aria-label="Show options"
          tabIndex={-1}
          className="text-muted-foreground hover:text-foreground rounded p-1"
          onPointerDown={(e) => {
            e.preventDefault()
            openBeforeToggle.current = open
          }}
          onClick={() => {
            input.current?.focus()
            if (openBeforeToggle.current) setOpen(false)
            else openAll()
          }}
        >
          <ChevronDown
            className={cn('size-4 transition-transform', open && 'rotate-180')}
          />
        </button>
      </div>
      {open && (matches.length > 0 || note) && (
        <div className="bg-popover absolute z-20 mt-1 max-h-64 w-full overflow-y-auto rounded-md border p-1 shadow-md">
          {note && <p className="text-muted-foreground px-2 py-1.5 text-xs">{note}</p>}
          {matches.map((option, i) => (
            <button
              key={option.value}
              type="button"
              role="option"
              aria-selected={i === active}
              className={cn(
                'flex w-full items-center justify-between gap-3 rounded-sm px-2 py-1.5 text-left text-xs',
                i === active && 'bg-accent text-accent-foreground',
              )}
              onPointerEnter={() => setActive(i)}
              onClick={() => pick(option)}
            >
              <span className="flex min-w-0 items-center gap-1.5">
                <Check
                  className={cn(
                    'size-3.5 shrink-0',
                    option.value !== value && 'invisible',
                  )}
                />
                <span className="truncate font-mono">{option.value}</span>
              </span>
              {option.hint && (
                <span className="text-muted-foreground shrink-0">{option.hint}</span>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

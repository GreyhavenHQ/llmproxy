import { useCallback, useSyncExternalStore } from 'react'

// Filters live in the query string, so they survive a pane switch, a reload and a shared link.
const CHANGE = 'llmproxy:querychange'

function subscribe(onChange: () => void) {
  window.addEventListener('popstate', onChange)
  window.addEventListener(CHANGE, onChange)
  return () => {
    window.removeEventListener('popstate', onChange)
    window.removeEventListener(CHANGE, onChange)
  }
}

const getSearch = () => window.location.search

export function useQueryParam(name: string, fallback = ''): [string, (v: string) => void] {
  const search = useSyncExternalStore(subscribe, getSearch)
  const value = new URLSearchParams(search).get(name) ?? fallback
  const set = useCallback(
    (v: string) => {
      const params = new URLSearchParams(window.location.search)
      if (v) params.set(name, v)
      else params.delete(name)
      const query = params.toString()
      window.history.replaceState(null, '', window.location.pathname + (query ? '?' + query : ''))
      window.dispatchEvent(new Event(CHANGE))
    },
    [name],
  )
  return [value, set]
}

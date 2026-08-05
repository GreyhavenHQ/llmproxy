import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError } from './api'

// Minimal load-and-reload hook; enough for an admin surface, no cache layer.
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const fnRef = useRef(fn)
  fnRef.current = fn

  const reload = useCallback(() => {
    setLoading(true)
    fnRef
      .current()
      .then((d) => {
        setData(d)
        setError(null)
      })
      .catch((e: unknown) => {
        setError(e instanceof ApiError ? e.message : String(e))
      })
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  useEffect(() => {
    reload()
  }, [reload])

  return { data, error, loading, reload }
}

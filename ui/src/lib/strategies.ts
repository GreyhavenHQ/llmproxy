export const STRATEGIES = [
  { value: 'failover', label: 'Failover', hint: 'Use the first target. Try the next one if it fails.' },
  { value: 'round_robin', label: 'Round robin', hint: 'Send each request to the next target in turn.' },
  { value: 'weighted', label: 'Weighted', hint: 'Spread requests by weight.' },
  { value: 'least_busy', label: 'Least busy', hint: 'Send each request to the target with the fewest open requests.' },
]

export function strategyLabel(strategy: string | null): string {
  return STRATEGIES.find((s) => s.value === strategy)?.label ?? strategy ?? ''
}

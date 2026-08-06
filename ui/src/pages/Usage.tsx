import { UsageDashboard } from '@/components/UsageDashboard'

export function Usage({ ssoEnabled }: { ssoEnabled: boolean }) {
  return <UsageDashboard ssoEnabled={ssoEnabled} />
}

import { useCallback, useEffect, useState } from 'react'
import { api, type Me } from '@/lib/api'
import { Logo } from '@/components/Logo'
import { Login } from '@/pages/Login'
import { Keys } from '@/pages/Keys'
import { Usage } from '@/pages/Usage'
import { Providers } from '@/pages/Providers'
import { Models } from '@/pages/Models'
import { Requests } from '@/pages/Requests'
import { Users } from '@/pages/Users'
import { Services } from '@/pages/Services'
import { AllKeys } from '@/pages/AllKeys'
import { Playground } from '@/pages/Playground'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useTheme, type Theme } from '@/lib/theme'
import { CircleUserRound, Moon, Sun, SunMoon } from 'lucide-react'

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [failed, setFailed] = useState(false)

  const loadMe = useCallback(() => {
    api
      .get<Me>('/auth/me')
      .then(setMe)
      .catch(() => setFailed(true))
  }, [])

  useEffect(loadMe, [loadMe])

  if (failed) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <p className="text-sm text-muted-foreground">
          The proxy is not reachable. Reload to retry.
        </p>
      </div>
    )
  }
  if (me === null) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spinner className="size-6" />
      </div>
    )
  }
  if (!me.authenticated) {
    return <Login me={me} onLogin={loadMe} />
  }
  return <Shell me={me} />
}

// One button, cycling light -> dark -> system; the icon names the active mode.
function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const next: Record<Theme, Theme> = { light: 'dark', dark: 'system', system: 'light' }
  const icons: Record<Theme, React.ReactNode> = {
    light: <Sun />,
    dark: <Moon />,
    system: <SunMoon />,
  }
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={`Theme: ${theme}. Switch to ${next[theme]}`}
      title={`Theme: ${theme}`}
      onClick={() => setTheme(next[theme])}
    >
      {icons[theme]}
    </Button>
  )
}

// The tab rides in the URL path (/usage, /models, ...) so reloads, copied
// links and back/forward all land on the right tab. The server serves
// index.html for any non-file GET, so these paths resolve without a router.
// The admin pages (/providers, /models, /users) share one top-level "Admin"
// tab with a sub-nav, keeping the member-facing nav identical for everyone.
const ADMIN_SUBS = ['providers', 'models', 'users', 'services', 'all-keys']

function tabsFromLocation(isAdmin: boolean): { tab: string; sub: string } {
  let seg = window.location.pathname.replace(/^\/+/, '').split('/')[0]
  if (seg === 'activity') seg = 'requests' // pre-rename bookmarks
  if (isAdmin && ADMIN_SUBS.includes(seg)) {
    return { tab: 'admin', sub: seg }
  }
  return {
    tab: ['usage', 'requests', 'keys', 'playground'].includes(seg) ? seg : 'usage',
    sub: 'providers',
  }
}

function Shell({ me }: { me: Me }) {
  const isAdmin = me.role === 'admin'
  const [nav, setNav] = useState(() => tabsFromLocation(isAdmin))

  useEffect(() => {
    const onPop = () => setNav(tabsFromLocation(isAdmin))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [isAdmin])

  const push = (next: { tab: string; sub: string }) => {
    setNav(next)
    const path = '/' + (next.tab === 'admin' ? next.sub : next.tab)
    if (window.location.pathname !== path) {
      window.history.pushState(null, '', path)
    }
  }
  const navigate = (tab: string) => push({ ...nav, tab })
  const navigateSub = (sub: string) => push({ tab: 'admin', sub })

  return (
    <Tabs value={nav.tab} onValueChange={navigate}>
      <header className="border-b bg-card">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
          <div className="flex items-center gap-2">
            <Logo className="h-6" />
            <span className="font-serif text-lg font-semibold">llmproxy</span>
          </div>
          <TabsList>
            <TabsTrigger value="usage">Usage</TabsTrigger>
            <TabsTrigger value="requests">Requests</TabsTrigger>
            <TabsTrigger value="keys">API keys</TabsTrigger>
            <TabsTrigger value="playground">Playground</TabsTrigger>
            {isAdmin && <TabsTrigger value="admin">Admin</TabsTrigger>}
          </TabsList>
          <div className="ml-auto flex items-center gap-1">
            <ThemeToggle />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" className="gap-2">
                  <CircleUserRound />
                  <span className="max-w-40 truncate">{me.name}</span>
                  {isAdmin && <Badge variant="secondary">admin</Badge>}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuLabel className="font-normal text-muted-foreground">
                  Signed in as {me.name}
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={() => (window.location.href = '/auth/logout')}>
                  Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </header>
      {/* w-full because mx-auto on a flex item would otherwise shrink the
          main column to its content and every tab would be a different width. */}
      <main className="mx-auto flex w-full max-w-6xl flex-col gap-6 px-4 py-8">
        <TabsContent value="usage">
          <Usage ssoEnabled={me.sso_enabled} />
        </TabsContent>
        <TabsContent value="requests">
          <Requests />
        </TabsContent>
        <TabsContent value="keys">
          <Keys />
        </TabsContent>
        {/* forceMount so switching tabs does not wipe the (unsaved) chat. */}
        <TabsContent value="playground" forceMount className="data-[state=inactive]:hidden">
          <Playground />
        </TabsContent>
        {isAdmin && (
          <TabsContent value="admin">
            <Tabs value={nav.sub} onValueChange={navigateSub} className="gap-6">
              <TabsList>
                <TabsTrigger value="providers">Providers</TabsTrigger>
                <TabsTrigger value="models">Models</TabsTrigger>
                <TabsTrigger value="users">Users</TabsTrigger>
                <TabsTrigger value="services">Services</TabsTrigger>
                <TabsTrigger value="all-keys">All keys</TabsTrigger>
              </TabsList>
              <TabsContent value="providers">
                <Providers />
              </TabsContent>
              <TabsContent value="models">
                <Models />
              </TabsContent>
              <TabsContent value="users">
                <Users />
              </TabsContent>
              <TabsContent value="services">
                <Services />
              </TabsContent>
              <TabsContent value="all-keys">
                <AllKeys />
              </TabsContent>
            </Tabs>
          </TabsContent>
        )}
      </main>
    </Tabs>
  )
}

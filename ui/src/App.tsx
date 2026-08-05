import { useCallback, useEffect, useState } from 'react'
import { api, type Me } from '@/lib/api'
import { Logo } from '@/components/Logo'
import { Login } from '@/pages/Login'
import { Keys } from '@/pages/Keys'
import { Usage } from '@/pages/Usage'
import { Providers } from '@/pages/Providers'
import { Models } from '@/pages/Models'
import { Requests } from '@/pages/Requests'
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
function tabFromLocation(allowed: string[]): string {
  let seg = window.location.pathname.replace(/^\/+/, '').split('/')[0]
  if (seg === 'activity') seg = 'requests' // pre-rename bookmarks
  return allowed.includes(seg) ? seg : 'usage'
}

function Shell({ me }: { me: Me }) {
  const isAdmin = me.role === 'admin'
  const allowedTabs = isAdmin
    ? ['usage', 'requests', 'keys', 'providers', 'models']
    : ['usage', 'requests', 'keys']
  const [tab, setTab] = useState(() => tabFromLocation(allowedTabs))

  useEffect(() => {
    const onPop = () => setTab(tabFromLocation(allowedTabs))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAdmin])

  const navigate = (next: string) => {
    setTab(next)
    if (tabFromLocation(allowedTabs) !== next) {
      window.history.pushState(null, '', '/' + next)
    }
  }

  return (
    <Tabs value={tab} onValueChange={navigate}>
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
            {isAdmin && <TabsTrigger value="providers">Providers</TabsTrigger>}
            {isAdmin && <TabsTrigger value="models">Models</TabsTrigger>}
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
          <Usage />
        </TabsContent>
        <TabsContent value="requests">
          <Requests />
        </TabsContent>
        <TabsContent value="keys">
          <Keys />
        </TabsContent>
        {isAdmin && (
          <>
            <TabsContent value="providers">
              <Providers />
            </TabsContent>
            <TabsContent value="models">
              <Models />
            </TabsContent>
          </>
        )}
      </main>
    </Tabs>
  )
}

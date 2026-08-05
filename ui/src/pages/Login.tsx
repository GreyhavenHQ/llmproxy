import { useState } from 'react'
import { api, ApiError, type Me } from '@/lib/api'
import { Logo } from '@/components/Logo'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { Separator } from '@/components/ui/separator'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

export function Login({ me, onLogin }: { me: Me; onLogin: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api.post('/auth/password', { password })
      onLogin()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'login failed')
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="items-center text-center">
          <Logo className="mx-auto mb-2 h-10" />
          <CardTitle className="font-serif text-2xl">llmproxy</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {me.sso_enabled && (
            <Button className="w-full" onClick={() => (window.location.href = '/auth/login')}>
              Sign in with SSO
            </Button>
          )}
          {me.sso_enabled && me.password_enabled && (
            <div className="flex items-center gap-3">
              <Separator className="flex-1" />
              <span className="text-xs text-muted-foreground">or</span>
              <Separator className="flex-1" />
            </div>
          )}
          {me.password_enabled && (
            <form onSubmit={submit} className="flex flex-col gap-4">
              <div className="flex flex-col gap-2">
                <Label htmlFor="admin-password">Admin password</Label>
                <Input
                  id="admin-password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  aria-invalid={error != null}
                />
                {error && <p className="text-sm text-destructive">{error}</p>}
              </div>
              <Button
                type="submit"
                variant={me.sso_enabled ? 'outline' : 'default'}
                disabled={busy || password === ''}
                className="w-full"
              >
                {busy && <Spinner />}
                Sign in as admin
              </Button>
            </form>
          )}
          {!me.sso_enabled && !me.password_enabled && (
            <p className="text-sm text-muted-foreground">
              Password login is disabled and no SSO is configured. Mint a key
              with <code className="font-mono">llmproxy key create</code> and
              use the API directly.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

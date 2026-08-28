import { toast } from 'sonner'
import { api, type Principal } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export function Users() {
  const { data, loading, error } = useAsync(() =>
    api.get<{ principals: Principal[] }>('/admin/v1/principals?limit=500'),
  )
  const users = data?.principals.filter((p) => p.kind === 'user') ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">Users</CardTitle>
        <CardDescription>People appear after their first sign-in.</CardDescription>
      </CardHeader>
      <CardContent>
        {loading && !data ? (
          <Spinner />
        ) : error ? (
          <p className="text-sm text-destructive">{error}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Role</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.length === 0 && (
                <TableRow>
                  <TableCell colSpan={3} className="text-muted-foreground">
                    No users yet.
                  </TableCell>
                </TableRow>
              )}
              {users.map((p) => (
                <TableRow key={p.id}>
                  <TableCell className="wrap-anywhere">{p.name}</TableCell>
                  <TableCell>
                    {p.role === 'admin' ? (
                      <Badge variant="secondary">admin</Badge>
                    ) : (
                      'member'
                    )}
                  </TableCell>
                  <TableCell className="text-right whitespace-nowrap">
                    <RevokeSessionsButton principal={p} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function RevokeSessionsButton({ principal }: { principal: Principal }) {
  const revoke = async () => {
    try {
      await api.post(`/admin/v1/principals/${principal.id}/revoke-sessions`)
      toast.success(`Signed out ${principal.name}`)
    } catch (err) {
      toast.error(errMsg(err))
    }
  }
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="sm">
          Sign out
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Sign out {principal.name} everywhere?</AlertDialogTitle>
          <AlertDialogDescription>
            Their sessions end now. API keys keep working.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={revoke}>Sign out</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

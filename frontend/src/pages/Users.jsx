import { useCallback, useEffect, useState } from 'react'
import { Users as UsersIcon } from 'lucide-react'
import { useAuth } from '../auth'
import { fmt } from './Account'
import { size } from '../lib/size'
import { PageHeader, EmptyState, SectionTitle, Stat } from '@/components/page-shell'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

/* Admin-only: who has access to this Hub, and what they have been using.
 *
 * This is the only page in the Hub that is not open to every approved
 * account — everything else (installing, launching, stopping) stays available
 * to any active user. */
export default function Users() {
  const me = useAuth((s) => s.user)
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const [creating, setCreating] = useState(false)

  const load = useCallback(async () => {
    const res = await fetch('/api/admin/users')
    if (!res.ok) {
      setError(res.status === 403 ? 'Only an administrator can view this page.' : 'Could not load users.')
      return
    }
    setData(await res.json())
    setError(null)
  }, [])

  useEffect(() => { load() }, [load])

  if (error) {
    return (
      <div className="page">
        <p className="m-0 text-sm leading-relaxed text-muted-foreground">{error}</p>
      </div>
    )
  }

  if (!data) {
    return (
      <div className="page flex flex-col gap-3">
        <p className="m-0 text-sm text-muted-foreground">Loading users…</p>
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex items-center gap-3 border-b border-border py-3">
            <Skeleton className="size-9 shrink-0 rounded-full" />
            <div className="flex flex-1 flex-col gap-2">
              <Skeleton className="h-3.5 w-40" />
              <Skeleton className="h-3 w-56" />
            </div>
            <Skeleton className="h-5 w-16" />
          </div>
        ))}
      </div>
    )
  }

  const pending = data.users.filter((u) => u.status === 'pending')
  const rest = data.users.filter((u) => u.status !== 'pending')

  return (
    <div className="page flex flex-col gap-6">
      <PageHeader
        title="Users"
        description="Approve access and see how the Spark is being used."
        actions={<Button onClick={() => setCreating(true)}>Add user</Button>}
      />

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
        <Stat label="Users" value={fmt(data.totals.users)} />
        <Stat label="Awaiting approval" value={fmt(data.totals.pending)} />
        <Stat label="Requests" value={fmt(data.totals.requests)} />
        <Stat label="Tokens" value={fmt(data.totals.total_tokens)} />
        <Stat label="Files" value={size(data.totals.storage_bytes)} />
      </div>

      {pending.length > 0 && (
        <section className="flex flex-col gap-3">
          <SectionTitle hint="These people signed up and cannot sign in until you let them.">
            {`Waiting for approval · ${pending.length}`}
          </SectionTitle>
          <Card className="gap-0 p-5 shadow-none">
            <Table>
              <UserTableHead />
              <TableBody>
                {pending.map((u) => (
                  <UserRow key={u.id} user={u} me={me} onChanged={load} />
                ))}
              </TableBody>
            </Table>
          </Card>
        </section>
      )}

      <section className="flex flex-col gap-3">
        <SectionTitle hint="Sorted by how much of the Spark each account has used.">
          Accounts
        </SectionTitle>
        {rest.length === 0 ? (
          <EmptyState
            icon={<UsersIcon className="h-6 w-6" />}
            title="No approved accounts yet."
          />
        ) : (
          <Card className="gap-0 p-5 shadow-none">
            <Table>
              <UserTableHead />
              <TableBody>
                {rest.map((u) => (
                  <UserRow key={u.id} user={u} me={me} onChanged={load} />
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </section>

      {creating && (
        <CreateUserModal onClose={() => setCreating(false)} onCreated={() => { setCreating(false); load() }} />
      )}
    </div>
  )
}

/* ── the shared column headings ──────────────────────────────────────────── */

function UserTableHead() {
  return (
    <TableHeader>
      <TableRow className="hover:bg-transparent">
        <TableHead className="h-8 pl-0 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
          Account
        </TableHead>
        <TableHead className="hidden h-8 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground sm:table-cell">
          requests
        </TableHead>
        <TableHead className="hidden h-8 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground md:table-cell">
          tokens
        </TableHead>
        <TableHead className="hidden h-8 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground lg:table-cell">
          30d
        </TableHead>
        <TableHead className="hidden h-8 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground sm:table-cell">
          files
        </TableHead>
        <TableHead className="hidden h-8 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground md:table-cell">
          role
        </TableHead>
        <TableHead className="h-8 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
          status
        </TableHead>
        <TableHead className="h-8 pr-0 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
          actions
        </TableHead>
      </TableRow>
    </TableHeader>
  )
}

/* ── one account ─────────────────────────────────────────────────────────── */

const STATUS_STYLE = {
  pending: 'border-warning/30 bg-warning/10 text-warning',
  active: 'border-success/30 bg-success/10 text-success',
  rejected: 'border-destructive/30 bg-destructive/10 text-destructive',
}

function UserRow({ user, me, onChanged }) {
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const isSelf = user.id === me?.id

  const patch = async (body, confirmText) => {
    if (confirmText && !window.confirm(confirmText)) return
    setBusy(true)
    try {
      const res = await fetch(`/api/admin/users/${user.id}`, {
        method: 'PATCH',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const err = await res.json().catch(() => ({}))
        window.alert(err.detail || 'Could not update the account.')
      }
      await onChanged()
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    setBusy(true)
    try {
      const res = await fetch(`/api/admin/users/${user.id}`, { method: 'DELETE' })
      if (!res.ok) {
        const err = await res.json().catch(() => ({}))
        window.alert(err.detail || 'Could not delete the account.')
      }
      await onChanged()
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <TableRow className={open ? 'bg-muted/50' : undefined}>
        <TableCell className="pl-0 whitespace-normal">
          <div className="flex items-center gap-3">
            <Avatar className="size-9">
              <AvatarFallback className="bg-muted text-[11px] font-medium text-muted-foreground">
                {initials(user)}
              </AvatarFallback>
            </Avatar>
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium text-foreground">
                  {user.name || user.email}
                </span>
                {isSelf && (
                  <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">you</span>
                )}
              </div>
              <div className="truncate font-mono text-[11px] text-muted-foreground">{user.email}</div>
              {user.storage.files > 0 && (
                <div className="text-[11px] text-muted-foreground sm:hidden">
                  {size(user.storage.bytes)} of files
                </div>
              )}
            </div>
          </div>
        </TableCell>

        <TableCell className="hidden text-right font-mono text-xs tabular-nums text-foreground sm:table-cell">
          {fmt(user.usage.requests)}
        </TableCell>
        <TableCell className="hidden text-right font-mono text-xs tabular-nums text-foreground md:table-cell">
          {fmt(user.usage.total_tokens)}
        </TableCell>
        <TableCell className="hidden text-right font-mono text-xs tabular-nums text-foreground lg:table-cell">
          {fmt(user.usage.tokens_30d)}
        </TableCell>
        <TableCell className="hidden text-right font-mono text-xs tabular-nums text-foreground sm:table-cell">
          {user.storage.files ? size(user.storage.bytes) : '0'}
        </TableCell>

        <TableCell className="hidden md:table-cell">
          {user.role === 'admin' && (
            <Badge variant="secondary" className="text-[10px] uppercase tracking-[0.12em]">
              admin
            </Badge>
          )}
        </TableCell>
        <TableCell>
          <Badge
            variant="outline"
            className={`text-[10px] uppercase tracking-[0.12em] ${STATUS_STYLE[user.status] || ''}`}
          >
            {user.status}
          </Badge>
        </TableCell>

        <TableCell className="pr-0 text-right">
          {user.status === 'pending' ? (
            <div className="flex justify-end gap-2">
              <Button size="sm" disabled={busy} onClick={() => patch({ status: 'active' })}>
                Approve
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={busy}
                onClick={() => patch({ status: 'rejected' }, `Reject ${user.email}? They will not be able to sign in.`)}
              >
                Reject
              </Button>
            </div>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setOpen(!open)}>
              {open ? 'Close' : 'Manage'}
            </Button>
          )}
        </TableCell>
      </TableRow>

      {open && (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={8} className="p-0">
            <div className="flex flex-col gap-3 border-t border-border bg-muted/30 px-4 pb-4 pt-4">
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <Stat className="border-border bg-background px-3 py-2" label="Requests" value={fmt(user.usage.requests)} />
                <Stat className="border-border bg-background px-3 py-2" label="Tokens in" value={fmt(user.usage.prompt_tokens)} />
                <Stat className="border-border bg-background px-3 py-2" label="Tokens out" value={fmt(user.usage.completion_tokens)} />
                <Stat className="border-border bg-background px-3 py-2" label="Tokens · 24h" value={fmt(user.usage.tokens_24h)} />
              </div>
              <p className="m-0 text-[11px] text-muted-foreground">
                Joined {shortDate(user.created_at)}
                {user.last_login_at && ` · last signed in ${shortDate(user.last_login_at)}`}
                {user.usage.last_used_at && ` · last model call ${shortDate(user.usage.last_used_at)}`}
              </p>

              <div className="flex flex-wrap items-center gap-2">
                {user.status === 'active' ? (
                  <Button
                    size="sm"
                    variant="destructive"
                    disabled={busy}
                    onClick={() => patch({ status: 'rejected' }, `Revoke access for ${user.email}? They are signed out immediately and their API key stops working.`)}
                  >
                    Revoke access
                  </Button>
                ) : (
                  <Button size="sm" disabled={busy} onClick={() => patch({ status: 'active' })}>
                    Restore access
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() => patch(
                    { role: user.role === 'admin' ? 'user' : 'admin' },
                    user.role === 'admin'
                      ? `Remove administrator rights from ${user.email}?`
                      : `Make ${user.email} an administrator? They will be able to approve and remove users, including you.`
                  )}
                >
                  {user.role === 'admin' ? 'Remove admin' : 'Make admin'}
                </Button>
                <ResetPassword userId={user.id} email={user.email} />
                {!isSelf && (
                  <Button size="sm" variant="destructive" disabled={busy} onClick={() => setConfirmDelete(true)}>
                    Delete
                  </Button>
                )}
              </div>
            </div>
          </TableCell>
        </TableRow>
      )}

      {confirmDelete && (
        <AlertDialog open onOpenChange={(next) => { if (!next) setConfirmDelete(false) }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{`Delete ${user.email}?`}</AlertDialogTitle>
              <AlertDialogDescription>
                Their API key stops working immediately and their usage history is removed.
                This cannot be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={busy} onClick={() => setConfirmDelete(false)}>
                Cancel
              </AlertDialogCancel>
              <AlertDialogAction variant="destructive" disabled={busy} onClick={remove}>
                Delete
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </>
  )
}

function ResetPassword({ userId, email }) {
  const [value, setValue] = useState('')
  const [open, setOpen] = useState(false)
  const [msg, setMsg] = useState(null)

  if (!open) {
    return (
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Set password
      </Button>
    )
  }

  const save = async () => {
    const res = await fetch(`/api/admin/users/${userId}`, {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ password: value }),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({}))
      setMsg(err.detail || 'Could not set the password.')
      return
    }
    setValue(''); setOpen(false); setMsg(null)
    window.alert(`Password updated. ${email} has been signed out and must sign in again with the new password.`)
  }

  return (
    <div className="flex items-center gap-2">
      <Input
        type="text" value={value} onChange={(e) => setValue(e.target.value)}
        placeholder="New password" autoComplete="off"
        className="h-8 w-40 text-xs"
      />
      <Button size="sm" onClick={save} disabled={value.length < 8}>
        Set
      </Button>
      <Button size="sm" variant="outline" onClick={() => { setOpen(false); setMsg(null) }}>
        Cancel
      </Button>
      {msg && <span className="text-[11px] leading-relaxed text-destructive">{msg}</span>}
    </div>
  )
}

/* ── create ──────────────────────────────────────────────────────────────── */

function CreateUserModal({ onClose, onCreated }) {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('user')
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e) => {
    e.preventDefault()
    setBusy(true); setError(null)
    const res = await fetch('/api/admin/users', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ name, email, password, role, status: 'active' }),
    })
    setBusy(false)
    if (!res.ok) {
      const err = await res.json().catch(() => ({}))
      setError(err.detail || 'Could not create the account.')
      return
    }
    onCreated()
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className="gap-4">
        <DialogHeader>
          <DialogTitle className="font-display text-sm font-semibold tracking-tight">
            Add a user
          </DialogTitle>
          <DialogDescription className="text-[11px] leading-relaxed">
            The account is active right away — no approval step. Send them the email and
            password you set here; they can change both from their Account page.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={submit} className="flex flex-col gap-3">
          <Field label="Name" value={name} onChange={setName} placeholder="Optional" />
          <Field label="Email" type="email" value={email} onChange={setEmail} required />
          <Field label="Password" type="text" value={password} onChange={setPassword}
                 hint="At least 8 characters." autoComplete="off" required />

          <div className="flex flex-col gap-1.5">
            <Label className="text-[11px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
              Role
            </Label>
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger className="h-9 w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">User — full access to apps and models</SelectItem>
                <SelectItem value="admin">Admin — can also manage users</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {error && (
            <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs leading-relaxed text-destructive">
              {error}
            </div>
          )}

          <div className="mt-1 flex gap-2">
            <Button type="submit" disabled={busy} className="flex-1">
              {busy ? 'Creating…' : 'Create user'}
            </Button>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ── bits ────────────────────────────────────────────────────────────────── */

function Field({ label, hint, value, onChange, ...rest }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label className="text-[11px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
        {label}
      </Label>
      <Input
        {...rest}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-9 placeholder:text-muted-foreground/70"
      />
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </div>
  )
}

function initials(user) {
  return (user.name || user.email || '?').trim().slice(0, 1).toUpperCase()
}

function shortDate(s) {
  if (!s) return ''
  // SQLite hands back "YYYY-MM-DD HH:MM:SS" in UTC with no zone marker; say so
  // explicitly or the browser reads it as local time and shifts every date.
  const d = new Date(s.replace(' ', 'T') + 'Z')
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleDateString([], { year: 'numeric', month: 'short', day: 'numeric' })
}

import { useEffect, useState } from 'react'
import { useAuth } from '../auth'
import { copyText } from '../lib/clipboard'
import { maskKey } from '../lib/secret'
import { PageHeader, SectionTitle, Stat } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card as CardSurface } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'

/* Your own account: who you are, how you sign in, and the API key every
 * client on this Hub authenticates with. */
export default function Account() {
  const user = useAuth((s) => s.user)
  const refreshUser = useAuth((s) => s.refreshUser)
  const [me, setMe] = useState(null)

  useEffect(() => {
    let alive = true
    fetch('/api/auth/me')
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => alive && d && setMe(d))
    return () => { alive = false }
  }, [])

  if (!me) {
    return (
      <div className="page flex max-w-2xl flex-col gap-3">
        <p className="m-0 text-sm text-muted-foreground">Loading account…</p>
        <div className="flex flex-col gap-3">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-24 w-full" />
          ))}
        </div>
      </div>
    )
  }

  return (
    <div className="page flex max-w-2xl flex-col gap-6">
      <PageHeader
        title={me.name || me.email}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <span className="min-w-0 truncate">{me.email}</span>
            {me.role === 'admin' && (
              <Badge variant="outline" className="shrink-0 border-primary/30 bg-primary/10 align-middle text-[10px] font-semibold uppercase tracking-[0.12em] text-primary">
                Admin
              </Badge>
            )}
          </span>
        }
        actions={<Avatar user={me} size={56} />}
      />

      <ApiKeyCard apiKey={me.api_key} onRotated={(k) => setMe({ ...me, api_key: k })} />
      <UsageCard usage={me.usage} />
      <ProfileCard me={me} onSaved={(u) => { setMe({ ...me, ...u }); refreshUser() }} />
      <PasswordCard />
      <SignOutCard user={user} />
    </div>
  )
}

/* ── API key ─────────────────────────────────────────────────────────────── */

function ApiKeyCard({ apiKey, onRotated }) {
  const [shown, setShown] = useState(false)
  const [copied, setCopied] = useState(false)
  const [rotating, setRotating] = useState(false)

  const copy = async () => {
    if (await copyText(apiKey)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }

  const rotate = async () => {
    if (!window.confirm(
      'Generate a new API key?\n\nEvery client still using the old key will stop working until you paste the new one in.'
    )) return
    setRotating(true)
    try {
      const res = await fetch('/api/auth/me/api-key/rotate', { method: 'POST' })
      if (res.ok) {
        const data = await res.json()
        onRotated(data.api_key)
        setShown(true)
      }
    } finally {
      setRotating(false)
    }
  }

  const masked = maskKey(apiKey)

  return (
    <Card
      title="Your API key"
      subtitle="One key for every model this Hub serves. Paste it into any OpenAI- or Anthropic-compatible client — it keeps working when the running model changes."
    >
      <div className="flex flex-wrap items-stretch gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap rounded-lg border border-border bg-muted px-3 py-2 font-mono text-xs text-foreground">
          {shown ? apiKey : masked}
        </code>
        <Button
          variant="outline"
          onClick={() => setShown(!shown)}
          title={shown ? 'Hide' : 'Reveal'}
          className="shrink-0 whitespace-nowrap"
        >
          {shown ? 'Hide' : 'Show'}
        </Button>
        <Button onClick={copy} className="shrink-0 whitespace-nowrap">
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>

      <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
        <p className="m-0 max-w-lg text-[11px] leading-relaxed text-muted-foreground">
          Treat it like a password. Anyone with this key can use the models on this Spark as you.
        </p>
        <Button variant="destructive" onClick={rotate} disabled={rotating} className="shrink-0 whitespace-nowrap">
          {rotating ? 'Generating…' : 'Generate new key'}
        </Button>
      </div>
    </Card>
  )
}

/* ── usage ───────────────────────────────────────────────────────────────── */

function UsageCard({ usage }) {
  return (
    <Card title="Your usage" subtitle="Model calls made with your key or from this browser.">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Requests" value={fmt(usage.requests)} />
        <Stat label="Tokens" value={fmt(usage.total_tokens)} />
        <Stat label="Tokens · 30d" value={fmt(usage.tokens_30d)} />
        <Stat label="Tokens · 24h" value={fmt(usage.tokens_24h)} />
      </div>
    </Card>
  )
}

/* ── profile ─────────────────────────────────────────────────────────────── */

function ProfileCard({ me, onSaved }) {
  const [name, setName] = useState(me.name || '')
  const [email, setEmail] = useState(me.email)
  const [currentPassword, setCurrentPassword] = useState('')
  const [state, setState] = useState({ error: null, ok: false, busy: false })

  const emailChanged = email.trim().toLowerCase() !== me.email.toLowerCase()
  const nameChanged = name !== (me.name || '')
  const dirty = emailChanged || nameChanged

  const save = async (e) => {
    e.preventDefault()
    setState({ error: null, ok: false, busy: true })
    const body = { name }
    if (emailChanged) {
      body.email = email
      body.current_password = currentPassword
    }
    const res = await fetch('/api/auth/me', {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({}))
      setState({ error: err.detail || 'Could not save.', ok: false, busy: false })
      return
    }
    const updated = await res.json()
    setCurrentPassword('')
    setState({ error: null, ok: true, busy: false })
    onSaved(updated)
  }

  return (
    <Card title="Profile">
      <form onSubmit={save} className="flex flex-col gap-3">
        <Field label="Name" value={name} onChange={setName} placeholder="Optional" />
        <Field label="Email" type="email" value={email} onChange={setEmail} required />
        {/* Only asked for when the email is actually changing — a stolen
            session must not be enough to move the account to a new address. */}
        {emailChanged && (
          <Field
            label="Current password" type="password"
            value={currentPassword} onChange={setCurrentPassword}
            hint="Required to change the email on your account."
            autoComplete="current-password" required
          />
        )}
        <FormFooter
          state={state} dirty={dirty}
          okText="Profile saved." label="Save changes"
        />
      </form>
    </Card>
  )
}

/* ── password ────────────────────────────────────────────────────────────── */

function PasswordCard() {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [state, setState] = useState({ error: null, ok: false, busy: false })

  const save = async (e) => {
    e.preventDefault()
    if (newPassword !== confirm) {
      setState({ error: 'The two new passwords do not match.', ok: false, busy: false })
      return
    }
    setState({ error: null, ok: false, busy: true })
    const res = await fetch('/api/auth/me', {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({}))
      setState({ error: err.detail || 'Could not change the password.', ok: false, busy: false })
      return
    }
    setCurrentPassword(''); setNewPassword(''); setConfirm('')
    setState({ error: null, ok: true, busy: false })
  }

  return (
    <Card title="Password">
      <form onSubmit={save} className="flex flex-col gap-3">
        <Field label="Current password" type="password" value={currentPassword}
               onChange={setCurrentPassword} autoComplete="current-password" required />
        <Field label="New password" type="password" value={newPassword}
               onChange={setNewPassword} autoComplete="new-password"
               hint="At least 8 characters." required />
        <Field label="Confirm new password" type="password" value={confirm}
               onChange={setConfirm} autoComplete="new-password" required />
        <FormFooter
          state={state} dirty={Boolean(currentPassword && newPassword)}
          okText="Password changed." label="Change password"
        />
      </form>
    </Card>
  )
}

function SignOutCard() {
  const logout = useAuth((s) => s.logout)
  return (
    <Card title="Session">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="m-0 text-[11px] leading-relaxed text-muted-foreground">
          Signs this browser out. Your API key keeps working.
        </p>
        <Button variant="outline" onClick={logout} className="shrink-0 whitespace-nowrap">Sign out</Button>
      </div>
    </Card>
  )
}

/* ── shared bits ─────────────────────────────────────────────────────────── */

// The metric tile is page-shell's now; the export stays so nothing that already
// reaches for it here changes.
export { Stat }

export function Card({ title, subtitle, children }) {
  return (
    <CardSurface className="gap-0 rounded-lg p-5 shadow-none">
      <SectionTitle>{title}</SectionTitle>
      {subtitle && (
        <p className="m-0 mb-4 max-w-2xl text-xs leading-relaxed text-muted-foreground">{subtitle}</p>
      )}
      <div>{children}</div>
    </CardSurface>
  )
}

export function Field({ label, hint, value, onChange, ...rest }) {
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

function FormFooter({ state, dirty, okText, label }) {
  return (
    <div className="flex flex-wrap items-center gap-3 pt-1">
      <Button type="submit" disabled={state.busy || !dirty}>
        {state.busy ? 'Saving…' : label}
      </Button>
      {state.error && <span className="text-xs leading-relaxed text-destructive">{state.error}</span>}
      {state.ok && <span className="text-xs text-primary">{okText}</span>}
    </div>
  )
}

export function Avatar({ user, size = 36 }) {
  const seed = user.email || ''
  // Deterministic hue from the email so each person keeps the same colour
  // everywhere they appear — here and in the admin user list.
  let h = 0
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) % 360
  const initials = (user.name || user.email || '?').trim().slice(0, 1).toUpperCase()
  return (
    <div
      className="flex shrink-0 items-center justify-center rounded-lg border border-border font-display font-bold"
      style={{
        width: size, height: size, fontSize: size * 0.4,
        background: `hsl(${h} 55% 22%)`, color: `hsl(${h} 80% 72%)`,
      }}
    >
      {initials}
    </div>
  )
}

// `fmt` is not a component and never was: Users.jsx imports it from here, so
// the export cannot move without touching another page.
// eslint-disable-next-line react-refresh/only-export-components
export function fmt(n) {
  const v = Number(n || 0)
  if (v >= 1e9) return `${(v / 1e9).toFixed(1)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(v)
}

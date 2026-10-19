import { useState } from 'react'
import { Clock } from 'lucide-react'
import { useAuth } from '../auth'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

/* The one screen an unauthenticated visitor can reach.
 *
 * It has three faces, chosen by what the Hub reports:
 *   - setup:    no accounts exist yet, so this visitor claims it as admin
 *   - sign in:  the normal case
 *   - request:  sign-up, which creates a pending account for an admin to approve
 */
export default function Login() {
  const needsSetup = useAuth((s) => s.needsSetup)
  const pendingNotice = useAuth((s) => s.pendingNotice)
  const login = useAuth((s) => s.login)
  const register = useAuth((s) => s.register)

  const [mode, setMode] = useState(needsSetup ? 'setup' : 'signin')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState(false)

  const isSetup = needsSetup || mode === 'setup'
  const isRegister = isSetup || mode === 'register'

  const submit = async (e) => {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      if (isRegister) await register({ email, password, name })
      else await login(email, password)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Shell>
      {pendingNotice ? (
        <div className="text-center">
          <div className="mx-auto mb-4 flex h-11 w-11 items-center justify-center rounded-md border border-warning/30 bg-warning/10 text-warning">
            <Clock className="h-5 w-5" />
          </div>
          <h2 className="mb-2 font-display text-base font-semibold tracking-tight">Waiting for approval</h2>
          <p className="text-sm leading-relaxed text-muted-foreground">{pendingNotice}</p>
          <Button
            variant="outline"
            className="mt-6 w-full"
            onClick={() => window.location.reload()}
          >
            Back to sign in
          </Button>
        </div>
      ) : (
        <>
          <div className="mb-6">
            <h2 className="font-display text-base font-semibold tracking-tight">
              {isSetup ? 'Create the administrator account' : isRegister ? 'Request access' : 'Sign in'}
            </h2>
            <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">
              {isSetup
                ? 'This Hub has no accounts yet. The first one you create owns it — it can approve everyone who signs up after you.'
                : isRegister
                  ? 'Your account will be created straight away, but an administrator has to approve it before you can sign in.'
                  : 'Sign in to use the apps and models on this Spark.'}
            </p>
          </div>

          <form onSubmit={submit} className="flex flex-col gap-4">
            {isRegister && (
              <Field label="Name" value={name} onChange={setName} autoComplete="name" placeholder="Optional" />
            )}
            <Field
              label="Email"
              type="email"
              value={email}
              onChange={setEmail}
              autoComplete="username"
              required
            />
            <Field
              label="Password"
              type="password"
              value={password}
              onChange={setPassword}
              autoComplete={isRegister ? 'new-password' : 'current-password'}
              hint={isRegister ? 'At least 8 characters.' : null}
              required
            />

            {error && (
              <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs leading-relaxed text-destructive">
                {error}
              </div>
            )}

            <Button type="submit" disabled={busy} className="mt-1 w-full">
              {busy ? 'Working…' : isSetup ? 'Create account & sign in' : isRegister ? 'Request access' : 'Sign in'}
            </Button>
          </form>

          {!needsSetup && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => { setMode(isRegister ? 'signin' : 'register'); setError(null) }}
              className="mt-3 w-full text-xs text-muted-foreground hover:text-foreground"
            >
              {isRegister ? 'Already have an account? Sign in' : "Don't have an account? Request access"}
            </Button>
          )}
        </>
      )}
    </Shell>
  )
}

function Shell({ children }) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background px-6 py-10 text-foreground">
      <div className="w-full max-w-[400px]">
        <div className="mb-6 flex items-center gap-3">
          <img
            src="/brand/spark-ai-hub-mark.svg"
            alt=""
            className="h-9 w-9 rounded-md border border-border bg-[#101a07] p-1"
          />
          <div>
            <div className="font-display text-sm font-semibold tracking-tight">Spark AI Hub</div>
            <div className="eyebrow mt-0.5">NVIDIA DGX Spark</div>
          </div>
        </div>
        <Card className="border-border bg-card shadow-none">
          <CardContent className="p-6">{children}</CardContent>
        </Card>
      </div>
    </div>
  )
}

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

import { useCallback, useEffect, useState } from 'react'
import { Cpu } from 'lucide-react'
import { useAuth } from '../auth'
import { useStore } from '../store'
import {
  deleteEngine,
  getEngines,
  installEngine,
  saveEngineSettings,
  updateEngine,
} from '../lib/api'
import { PageHeader, EmptyState } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
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

/* Admin-only: the model runtimes installed on this machine.
 *
 * A container per model carries the whole runtime — CUDA, Python, the engine
 * itself — into every image, which is most of the gigabytes and none of the
 * weights. An engine lives once under ~/.config/gospark/engines/<name>/<v>,
 * and a native recipe is then only the launch config pointed at it: which
 * checkpoint, which flags, which port. This page is where that runtime is
 * installed, updated and removed.
 *
 * Every action re-reads the list instead of predicting the outcome, because
 * the daemon owns the truth about versions and about what is still running.
 */
export default function Engines() {
  const user = useAuth((s) => s.user)
  const isAdmin = !user || user.role === 'admin'

  const [engines, setEngines] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  // The catalog is already polled by the shell; this page only needs it to
  // know which native recipe is up (the dashboard link points at its port).
  const recipes = useStore((s) => s.recipes)
  // One job at a time across the page, the way Settings does it: two installs
  // racing would fight over the same symlink.
  const [busy, setBusy] = useState(null) // { name, action }
  // name -> the daemon's own sentence, shown inside that engine's card.
  const [failures, setFailures] = useState({})
  const [confirm, setConfirm] = useState(null)

  const apply = useCallback((res) => {
    if (res.ok) {
      setEngines(res.data?.engines || [])
      setError('')
    } else {
      setError(res.error)
    }
    setLoading(false)
  }, [])

  useEffect(() => {
    // A non-admin gets the refusal below instead of four 403s.
    if (!isAdmin) return
    let cancelled = false
    ;(async () => {
      const res = await getEngines()
      if (!cancelled) apply(res)
    })()
    return () => { cancelled = true }
  }, [isAdmin, apply])

  const run = async (engine, action) => {
    setBusy({ name: engine.name, action })
    setFailures((prev) => ({ ...prev, [engine.name]: '' }))
    let res
    switch (action) {
      case 'install':
        res = await installEngine(engine.name)
        break
      case 'update':
        res = await updateEngine(engine.name)
        break
      default:
        res = await deleteEngine(engine.name)
    }
    setBusy(null)
    if (!res.ok) {
      // 409 means "already there" or "a native recipe is running on it"; the
      // daemon's sentence says which, and it belongs next to the button.
      setFailures((prev) => ({ ...prev, [engine.name]: res.error }))
      return
    }
    apply(await getEngines())
  }

  const confirmDelete = async () => {
    if (!confirm) return
    const engine = confirm
    setConfirm(null)
    await run(engine, 'delete')
  }

  // The flags are launch arguments, so saving one only affects the next
  // launch — the running recipe keeps what it started with.
  const setOption = async (engine, key, value) => {
    const next = { ...(engine.options || {}), [key]: value }
    setBusy({ name: engine.name, action: 'settings' })
    setFailures((prev) => ({ ...prev, [engine.name]: '' }))
    const res = await saveEngineSettings(engine.name, next)
    if (!res.ok) {
      setBusy(null)
      setFailures((prev) => ({ ...prev, [engine.name]: res.error }))
      return
    }
    apply(await getEngines())
  }

  const dashboardPort = (engine) => {
    const up = recipes.find((r) => r.running && r.native?.engine === engine.name)
    return up ? up.native?.port || up.ui?.port : 0
  }

  if (!isAdmin) {
    return (
      <div className="page">
        <p className="m-0 text-sm text-muted-foreground">Administrator rights required.</p>
      </div>
    )
  }

  return (
    <div className="page flex flex-col gap-6">
      <PageHeader
        title="Engines"
        description="Model runtimes are installed once on this machine. Recipes launch on them without containers."
      />

      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2">
          <p className="m-0 text-sm leading-relaxed text-destructive">{error}</p>
        </div>
      )}

      {loading && engines.length === 0 ? (
        <div className="flex flex-col gap-3">
          <p className="m-0 text-sm text-muted-foreground">Loading engines…</p>
          <div className="flex flex-col gap-3">
            {[0, 1].map((i) => (
              <Card key={i} className="gap-0 p-5 shadow-none">
                <div className="flex items-start justify-between gap-3">
                  <div className="flex flex-col gap-2">
                    <Skeleton className="h-4 w-32" />
                    <Skeleton className="h-5 w-24" />
                  </div>
                  <Skeleton className="h-8 w-24" />
                </div>
                <Skeleton className="mt-4 h-3 w-full" />
                <Skeleton className="mt-2 h-3 w-2/3" />
              </Card>
            ))}
          </div>
        </div>
      ) : engines.length === 0 ? (
        <EmptyState
          icon={<Cpu className="h-6 w-6" />}
          title="No engines."
        />
      ) : (
        <div className="flex flex-col gap-3">
          {engines.map((engine) => (
            <EngineCard
              key={engine.name}
              engine={engine}
              busy={busy?.name === engine.name ? busy.action : null}
              disabled={busy !== null}
              error={failures[engine.name] || ''}
              dashboardPort={dashboardPort(engine)}
              onToggle={(key, value) => setOption(engine, key, value)}
              onInstall={() => run(engine, 'install')}
              onUpdate={() => run(engine, 'update')}
              onDelete={() => setConfirm(engine)}
            />
          ))}
        </div>
      )}

      {confirm && (
        <AlertDialog open onOpenChange={(open) => { if (!open) setConfirm(null) }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{`Delete engine ${displayName(confirm.name)}?`}</AlertDialogTitle>
              <AlertDialogDescription>
                Native recipes stop launching on this engine, and the installed runtime is removed
                from this machine.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel
                disabled={busy?.name === confirm.name}
                onClick={() => setConfirm(null)}
              >
                Cancel
              </AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                disabled={busy?.name === confirm.name}
                onClick={confirmDelete}
              >
                {busy?.name === confirm.name ? 'Deleting…' : 'Delete'}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  )
}

function EngineCard({ engine, busy, disabled, error, dashboardPort, onToggle, onInstall, onUpdate, onDelete }) {
  const name = displayName(engine.name)
  // `latest` is what the release feed says; an install made offline may be
  // ahead of it, and then there is nothing to update to.
  const updateAvailable = Boolean(engine.installed && engine.latest && engine.latest !== engine.version)
  const options = engine.options || {}
  // The dashboard is served by the model server itself, on the recipe's port.
  const dashboardUrl = dashboardPort
    ? `${window.location.protocol}//${window.location.hostname}:${dashboardPort}/dashboard`
    : ''

  return (
    <Card className="gap-0 p-5 shadow-none">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="m-0 font-display text-sm font-semibold text-foreground">{name}</h2>
            {updateAvailable && (
              <Badge variant="outline" className="border-primary/30 bg-primary/10 text-primary">
                Update to {engine.latest} available
              </Badge>
            )}
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            <Badge variant="outline" className="font-mono text-[11px] tabular-nums">
              {engine.installed ? `Version ${engine.version || 'unknown'}` : 'Not installed'}
            </Badge>
            {engine.installed && engine.latest && !updateAvailable && (
              <Badge variant="secondary" className="text-[11px]">
                up to date
              </Badge>
            )}
          </div>
          {engine.installed && engine.path && (
            <p className="m-0 mt-2 break-all font-mono text-[11px] text-muted-foreground">{engine.path}</p>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-2">
          {!engine.installed && (
            <Button onClick={onInstall} disabled={disabled}>
              {busy === 'install' ? '⟳ Installing…' : 'Install'}
            </Button>
          )}
          {engine.installed && (
            <>
              <Button
                variant="outline"
                size="sm"
                onClick={onUpdate}
                disabled={disabled || !updateAvailable}
                className="whitespace-nowrap"
              >
                {busy === 'update' ? '⟳ Updating…' : 'Update'}
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={onDelete}
                disabled={disabled}
                className="whitespace-nowrap"
              >
                {busy === 'delete' ? '⟳ Deleting…' : 'Delete'}
              </Button>
            </>
          )}
        </div>
      </div>

      {engine.installed && (
        <div className="mt-4 flex flex-col gap-3 border-t border-border pt-4">
          <Toggle
            label="Cortheon"
            hint="Adds --cortheon: chat requests get Cortheon's reasoning program, it researches the web before building, and cortheon_search / cortheon_fetch run on this engine."
            checked={Boolean(options.cortheon)}
            disabled={disabled}
            onChange={(v) => onToggle('cortheon', v)}
          />
          <div className="flex flex-col gap-1">
            <Toggle
              label="Live dashboard"
              hint="Adds --dashboard: the engine serves its own live page at /dashboard."
              checked={Boolean(options.dashboard)}
              disabled={disabled}
              onChange={(v) => onToggle('dashboard', v)}
            />
            {options.dashboard && !dashboardUrl && (
              <p className="m-0 text-[11px] text-muted-foreground">
                Launch a native recipe on this engine to open the dashboard.
              </p>
            )}
            {dashboardUrl && (
              <a
                href={dashboardUrl}
                target="_blank"
                rel="noreferrer"
                className="break-all font-mono text-[11px] text-primary hover:underline"
              >
                {dashboardUrl}
              </a>
            )}
          </div>
          <p className="m-0 text-[11px] text-muted-foreground">
            Flags apply to the next launch of a native recipe on this engine; a running one keeps
            the flags it started with.
          </p>
        </div>
      )}

      {error && <p className="m-0 mt-3 text-xs leading-relaxed text-destructive">{error}</p>}
    </Card>
  )
}

// Toggle is a labelled row around the shadcn Switch: the label and its hint
// stay on the left, the control is the primitive's own, and callers still
// speak in `checked` / `onChange`.
function Toggle({ label, hint, checked, disabled, onChange }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <p className="m-0 text-xs font-semibold text-foreground">{label}</p>
        <p className="m-0 mt-0.5 text-[11px] leading-relaxed text-muted-foreground">{hint}</p>
      </div>
      <Switch
        checked={checked}
        disabled={disabled}
        onCheckedChange={(v) => onChange(v)}
        className="mt-0.5 shrink-0"
      />
    </div>
  )
}

// The daemon names engines by their directory ("tensorfold"); the product
// spells them a little differently, and only these two do.
function displayName(name) {
  const known = { tensorfold: 'TensorFold' }
  if (known[name]) return known[name]
  return name ? name.charAt(0).toUpperCase() + name.slice(1) : name
}

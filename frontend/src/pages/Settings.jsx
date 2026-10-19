import { useCallback, useEffect, useState } from 'react'
import { FolderOpen, GitBranch } from 'lucide-react'
import { useAuth } from '../auth'
import { useStore } from '../store'
import {
  addRegistries,
  deleteLocalRecipe,
  fetchLocalRecipes,
  fetchRegistries,
  removeRegistry,
  syncRegistries,
  syncRegistry,
} from '../lib/api'
import ConfirmDeleteModal from '../components/ConfirmDeleteModal'
import { EmptyState, PageHeader, SectionTitle } from '@/components/page-shell'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'

/* Admin-only: where recipes come from, and the ones this Hub owns.
 *
 * Recipes are not stored in the Hub any more. They are cloned from git
 * repositories ("sources") into ~/.config/gospark/registry/vendored/<id>, and
 * a source only moves when someone presses Sync — a Hub on a Spark should not
 * rewrite its own catalog while you are using it. Anything the user wants to
 * change lives in local/, which no sync ever touches; the way in is "Create
 * copy" on a recipe page, and this page is where those copies are listed and
 * deleted.
 */
export default function Settings() {
  const user = useAuth((s) => s.user)
  const fetchRecipes = useStore((s) => s.fetchRecipes)

  const [sources, setSources] = useState([])
  const [local, setLocal] = useState([])
  const [syncing, setSyncing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // The add form: the textarea, the lines it could not parse, and its own
  // in-flight state so a failing URL never blocks the rest of the page.
  const [urls, setUrls] = useState('')
  const [addError, setAddError] = useState('')
  const [lineErrors, setLineErrors] = useState([])
  const [adding, setAdding] = useState(false)

  // One job at a time: 'sync-all' or the id of a single source.
  const [busy, setBusy] = useState(null)
  // slug -> true while its deletion is in flight.
  const [deleting, setDeleting] = useState({})
  const [confirm, setConfirm] = useState(null) // { slug, name }
  const [deleteError, setDeleteError] = useState('')

  const isAdmin = !user || user.role === 'admin'

  // Both halves of the page arrive together: the source list and the local
  // copies are two readings of the same directory.
  useEffect(() => {
    // A non-admin has nothing to load: the page renders the refusal instead.
    if (!isAdmin) return
    let cancelled = false
    ;(async () => {
      const [reg, loc] = await Promise.all([fetchRegistries(), fetchLocalRecipes()])
      if (cancelled) return
      if (reg.ok) {
        setSources(reg.data?.sources || [])
        setSyncing(Boolean(reg.data?.syncing))
        setError('')
      } else {
        // The source list is the point of the page, so a failure here is
        // stated in place instead of blanking the form and local list with it.
        setError(reg.error)
      }
      if (loc.ok) setLocal(loc.data?.recipes || [])
      else setError((prev) => prev || loc.error)
      setLoading(false)
    })()
    return () => { cancelled = true }
  }, [isAdmin])

  const reloadLocal = useCallback(async () => {
    const loc = await fetchLocalRecipes()
    if (loc.ok) setLocal(loc.data?.recipes || [])
  }, [])

  const submitSources = async (e) => {
    e.preventDefault()
    const raw = urls.trim()
    if (!raw) return
    setAdding(true)
    setAddError('')
    setLineErrors([])
    const res = await addRegistries(raw)
    setAdding(false)
    if (!res.ok) {
      setAddError(res.error)
      return
    }
    setSources(res.data?.sources || [])
    setUrls('')
    setLineErrors(res.data?.errors || [])
    // The clone added recipes to the catalog; refresh it and the local list
    // (a source can shadow a local slug, which changes the counts).
    fetchRecipes()
    reloadLocal()
  }

  const runSync = async (id) => {
    setBusy(id || 'sync-all')
    if (!id) setSyncing(true)
    const res = id ? await syncRegistry(id) : await syncRegistries()
    setBusy(null)
    setSyncing(false)
    if (!res.ok) {
      setError(res.error)
      return
    }
    setError('')
    const list = id ? (res.data?.source ? [res.data.source] : []) : (res.data?.sources || [])
    if (id) {
      setSources((prev) => prev.map((s) => (s.id === id ? { ...s, ...list[0] } : s)))
    } else {
      setSources(list)
    }
    fetchRecipes()
  }

  const removeSource = async (source) => {
    const where = source.subpath ? `${source.url} (${source.subpath})` : source.url
    if (!window.confirm(`Remove this source?\n\n${where}\n\nThe cloned copy is deleted. Local recipes are kept.`)) return
    setBusy(source.id)
    const res = await removeRegistry(source.id)
    setBusy(null)
    if (!res.ok) {
      setError(res.error)
      return
    }
    setSources((prev) => prev.filter((s) => s.id !== source.id))
    setError('')
    fetchRecipes()
    reloadLocal()
  }

  const confirmDelete = async () => {
    if (!confirm) return
    const { slug } = confirm
    setDeleting((prev) => ({ ...prev, [slug]: true }))
    setDeleteError('')
    const res = await deleteLocalRecipe(slug)
    setDeleting((prev) => ({ ...prev, [slug]: false }))
    if (!res.ok) {
      // 409: installed or running. The daemon's sentence says which, and the
      // dialog stays open so it is read next to the button that failed.
      setDeleteError(res.error)
      return
    }
    setConfirm(null)
    setLocal((prev) => prev.filter((r) => r.slug !== slug))
    fetchRecipes()
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
        title="Settings"
        description="Where recipes come from, and the copies this Hub owns."
      />

      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2">
          <p className="m-0 text-sm leading-relaxed text-destructive">{error}</p>
        </div>
      )}

      {/* ─── Add sources ─── */}
      <Section
        title="Recipe sources"
        subtitle="A folder of recipes in a git repository. Several at once, separated by commas or new lines."
      >
        <form onSubmit={submitSources} className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label
              htmlFor="settings-source-urls"
              className="text-[11px] font-semibold uppercase tracking-[0.1em] text-muted-foreground"
            >
              Source URLs
            </Label>
            <Textarea
              id="settings-source-urls"
              value={urls}
              onChange={(e) => setUrls(e.target.value)}
              rows={3}
              spellCheck={false}
              placeholder="https://github.com/owner/repo/tree/main/registry/recipes"
              className="min-h-[76px] resize-y font-mono text-xs placeholder:text-muted-foreground/70"
            />
          </div>
          <p className="m-0 text-[11px] text-muted-foreground">
            One per line or comma-separated. A <span className="font-mono">/tree/&lt;branch&gt;/&lt;subfolder&gt;</span> link
            picks the branch and the recipe folder inside the repository.
          </p>
          <div className="flex flex-wrap items-center gap-3">
            <Button
              type="submit"
              disabled={adding || !urls.trim()}
            >
              {adding ? 'Adding…' : 'Add sources'}
            </Button>
            {adding && <span className="text-xs text-muted-foreground">Cloning… this can take a moment.</span>}
          </div>
          {addError && <p className="m-0 text-xs leading-relaxed text-destructive">{addError}</p>}
          {lineErrors.length > 0 && (
            <ul className="m-0 flex flex-col gap-1 pl-4">
              {lineErrors.map((e) => (
                <li key={`${e.url}-${e.error}`} className="break-all text-xs text-destructive">
                  <span className="font-mono">{e.url}</span> — {e.error}
                </li>
              ))}
            </ul>
          )}
        </form>
      </Section>

      {/* ─── Configured sources ─── */}
      <Section
        title={`Sources · ${sources.length}`}
        subtitle="A source updates only when you sync it. Recipes it carries are read-only copies."
        action={
          <Button
            variant="outline"
            size="sm"
            onClick={() => runSync(null)}
            disabled={busy === 'sync-all' || syncing || sources.length === 0}
            className="shrink-0 whitespace-nowrap"
          >
            {busy === 'sync-all' || syncing ? 'Syncing…' : 'Sync all'}
          </Button>
        }
      >
        {loading && sources.length === 0 ? (
          <div className="flex flex-col gap-3">
            <p className="m-0 text-sm text-muted-foreground">Loading sources…</p>
            {[0, 1].map((i) => (
              <div key={i} className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2.5">
                <div className="flex min-w-0 flex-col gap-1.5">
                  <Skeleton className="h-3.5 w-64 max-w-full" />
                  <Skeleton className="h-3 w-32" />
                </div>
                <Skeleton className="h-7 w-28 shrink-0" />
              </div>
            ))}
          </div>
        ) : sources.length === 0 ? (
          <EmptyState
            icon={<GitBranch className="h-6 w-6" />}
            title="No sources yet."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-8 pl-0 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                  Source
                </TableHead>
                <TableHead className="h-8 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                  Synced
                </TableHead>
                <TableHead className="hidden h-8 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground sm:table-cell">
                  Recipes
                </TableHead>
                <TableHead className="h-8 pr-0 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                  Actions
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sources.map((source) => (
                <SourceRow
                  key={source.id}
                  source={source}
                  busy={busy === source.id}
                  disabled={busy !== null}
                  onSync={() => runSync(source.id)}
                  onRemove={() => removeSource(source)}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </Section>

      {/* ─── Local recipes ─── */}
      <Section
        title={`Local recipes · ${local.length}`}
        subtitle="Your own copies, made with “Create copy” on a recipe page. Syncing never touches them."
      >
        {local.length === 0 ? (
          <EmptyState
            icon={<FolderOpen className="h-6 w-6" />}
            title="No local recipes yet."
            description="Open any recipe and press “Create copy” to make one."
          />
        ) : (
          <div className="flex flex-col">
            {local.map((recipe) => (
              <div
                key={recipe.slug}
                className="flex flex-wrap items-center gap-3 border-b border-border py-3 last:border-b-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-foreground">{recipe.name || recipe.slug}</div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                    <span className="font-mono">{recipe.slug}</span>
                    <span>{recipe.origin ? `copied from ${recipe.origin}` : 'own recipe'}</span>
                    {recipe.modified && <span>{formatDate(recipe.modified)}</span>}
                  </div>
                </div>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => {
                    setDeleteError('')
                    setConfirm({ slug: recipe.slug, name: recipe.name })
                  }}
                  disabled={!!deleting[recipe.slug]}
                  className="shrink-0 whitespace-nowrap"
                >
                  Delete
                </Button>
              </div>
            ))}
          </div>
        )}
      </Section>

      {confirm && (
        <ConfirmDeleteModal
          slug={confirm.slug}
          name={confirm.name}
          busy={!!deleting[confirm.slug]}
          error={deleteError}
          onCancel={() => {
            setConfirm(null)
            setDeleteError('')
          }}
          onConfirm={confirmDelete}
        />
      )}
    </div>
  )
}

function SourceRow({ source, busy, disabled, onSync, onRemove }) {
  const recipes = typeof source.recipes === 'number' ? source.recipes : null
  return (
    <TableRow>
      <TableCell className="max-w-0 pl-0 align-top">
        <div className="break-all text-sm text-foreground">{source.url}</div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          {source.ref && <span className="font-mono">{source.ref}</span>}
          {source.commit && <span className="font-mono">{source.commit}</span>}
          {source.subpath && <span className="font-mono">{source.subpath}</span>}
        </div>
        {source.error && (
          <p className="m-0 mt-1.5 break-words text-xs leading-relaxed text-destructive">{source.error}</p>
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap align-top text-[11px] text-muted-foreground">
        {`synced ${timeAgo(source.synced_at)}`}
      </TableCell>
      <TableCell className="hidden whitespace-nowrap align-top font-mono text-xs tabular-nums text-muted-foreground sm:table-cell">
        {recipes != null ? `${recipes} recipe${recipes === 1 ? '' : 's'}` : '—'}
      </TableCell>
      <TableCell className="pr-0 text-right align-top">
        <div className="flex shrink-0 items-center justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={onSync}
            disabled={disabled}
            className="whitespace-nowrap"
          >
            {busy ? 'Syncing…' : 'Sync'}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={onRemove}
            disabled={disabled}
            className="whitespace-nowrap"
          >
            Remove
          </Button>
        </div>
      </TableCell>
    </TableRow>
  )
}

function Section({ title, subtitle, action, children }) {
  return (
    <Card className="gap-0 p-5 shadow-none">
      <SectionTitle right={action}>{title}</SectionTitle>
      {subtitle && (
        <p className="m-0 mb-4 max-w-2xl text-xs leading-relaxed text-muted-foreground">{subtitle}</p>
      )}
      <div className={subtitle ? '' : 'mt-3'}>{children}</div>
    </Card>
  )
}

/* "synced 4 minutes ago" — coarse on purpose. What matters at a glance is
 * whether the clone is fresh or from last week, not the seconds. */
function timeAgo(iso) {
  if (!iso) return 'never'
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return 'never'
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (secs < 45) return 'just now'
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins} minute${mins === 1 ? '' : 's'} ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`
  const days = Math.round(hours / 24)
  if (days < 31) return `${days} day${days === 1 ? '' : 's'} ago`
  return formatDate(iso)
}

function formatDate(iso) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return String(iso)
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

import { useCallback, useEffect, useState } from 'react'
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
      <div className="px-6 py-20 text-center text-text-dim text-sm">
        Administrator rights required.
      </div>
    )
  }

  return (
    <div className="px-4 sm:px-6 py-6 pb-12 max-w-4xl mx-auto animate-fadeIn flex flex-col gap-4">
      <div className="mb-1">
        <h1 className="text-2xl font-bold tracking-tight font-display m-0">Settings</h1>
        <p className="text-sm text-text-dim m-0 mt-1">
          Where recipes come from, and the copies this Hub owns.
        </p>
      </div>

      {error && (
        <div className="rounded-2xl border border-error/30 bg-error-surface px-4 py-3">
          <p className="text-sm text-error m-0 leading-relaxed">{error}</p>
        </div>
      )}

      {/* ─── Add sources ─── */}
      <Section
        title="Recipe sources"
        subtitle="A folder of recipes in a git repository. Several at once, separated by commas or new lines."
      >
        <form onSubmit={submitSources} className="flex flex-col gap-2.5">
          <textarea
            value={urls}
            onChange={(e) => setUrls(e.target.value)}
            rows={3}
            spellCheck={false}
            placeholder="https://github.com/owner/repo/tree/main/registry/recipes"
            className="w-full px-3.5 py-2.5 bg-surface-high rounded-xl text-text text-sm font-mono outline-none border border-outline-dim focus:border-primary/40 focus:ring-2 focus:ring-primary/10 placeholder:text-text-dim transition-all resize-y"
          />
          <p className="text-[11px] text-text-dim m-0">
            One per line or comma-separated. A <span className="font-mono">/tree/&lt;branch&gt;/&lt;subfolder&gt;</span> link
            picks the branch and the recipe folder inside the repository.
          </p>
          <div className="flex flex-wrap items-center gap-3">
            <button
              type="submit"
              disabled={adding || !urls.trim()}
              className="btn-primary px-4 py-2 text-sm font-bold disabled:cursor-default"
            >
              {adding ? 'Adding…' : 'Add sources'}
            </button>
            {adding && <span className="text-xs text-text-dim font-label">Cloning… this can take a moment.</span>}
          </div>
          {addError && <p className="text-xs text-error m-0 leading-relaxed">{addError}</p>}
          {lineErrors.length > 0 && (
            <ul className="m-0 pl-4 flex flex-col gap-1">
              {lineErrors.map((e) => (
                <li key={`${e.url}-${e.error}`} className="text-xs text-error break-all">
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
          <button
            onClick={() => runSync(null)}
            disabled={busy === 'sync-all' || syncing || sources.length === 0}
            className="shrink-0 px-3.5 py-2 rounded-xl bg-primary text-primary-on border-none text-xs font-bold cursor-pointer hover:opacity-90 disabled:opacity-50 disabled:cursor-default transition-opacity whitespace-nowrap"
          >
            {busy === 'sync-all' || syncing ? 'Syncing…' : 'Sync all'}
          </button>
        }
      >
        {loading && sources.length === 0 ? (
          <p className="text-sm text-text-dim m-0">Loading sources…</p>
        ) : sources.length === 0 ? (
          <p className="text-sm text-text-dim m-0">No sources yet.</p>
        ) : (
          <div className="flex flex-col">
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
          </div>
        )}
      </Section>

      {/* ─── Local recipes ─── */}
      <Section
        title={`Local recipes · ${local.length}`}
        subtitle="Your own copies, made with “Create copy” on a recipe page. Syncing never touches them."
      >
        {local.length === 0 ? (
          <p className="text-sm text-text-dim m-0">
            No local recipes yet. Open any recipe and press “Create copy” to make one.
          </p>
        ) : (
          <div className="flex flex-col">
            {local.map((recipe) => (
              <div
                key={recipe.slug}
                className="flex flex-wrap items-center gap-3 py-3 border-b border-outline-dim last:border-b-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-semibold text-text truncate">{recipe.name || recipe.slug}</div>
                  <div className="text-[11px] text-text-dim mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1">
                    <span className="font-mono">{recipe.slug}</span>
                    <span>{recipe.origin ? `copied from ${recipe.origin}` : 'own recipe'}</span>
                    {recipe.modified && <span>{formatDate(recipe.modified)}</span>}
                  </div>
                </div>
                <button
                  onClick={() => {
                    setDeleteError('')
                    setConfirm({ slug: recipe.slug, name: recipe.name })
                  }}
                  disabled={!!deleting[recipe.slug]}
                  className="shrink-0 px-3.5 py-2 rounded-xl bg-error-surface text-error border border-error/30 text-xs font-semibold cursor-pointer hover:bg-error/15 disabled:opacity-50 transition-colors whitespace-nowrap"
                >
                  Delete
                </button>
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
    <div className="flex flex-wrap items-start gap-3 py-3 border-b border-outline-dim last:border-b-0">
      <div className="min-w-0 flex-1">
        <div className="text-sm text-text break-all">{source.url}</div>
        <div className="text-[11px] text-text-dim mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
          {source.ref && <span className="font-mono">{source.ref}</span>}
          {source.commit && <span className="font-mono">{source.commit}</span>}
          {source.subpath && <span className="font-mono">{source.subpath}</span>}
          <span>synced {timeAgo(source.synced_at)}</span>
          {recipes != null && <span>{recipes} recipe{recipes === 1 ? '' : 's'}</span>}
        </div>
        {source.error && (
          <p className="text-xs text-error mt-1.5 mb-0 leading-relaxed break-words">{source.error}</p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <button
          onClick={onSync}
          disabled={disabled}
          className="px-3.5 py-2 rounded-xl bg-surface-high text-text border border-outline-dim text-xs font-semibold cursor-pointer hover:bg-surface-highest disabled:opacity-50 disabled:cursor-default transition-colors whitespace-nowrap"
        >
          {busy ? 'Syncing…' : 'Sync'}
        </button>
        <button
          onClick={onRemove}
          disabled={disabled}
          className="px-3.5 py-2 rounded-xl bg-error-surface text-error border border-error/30 text-xs font-semibold cursor-pointer hover:bg-error/15 disabled:opacity-50 disabled:cursor-default transition-colors whitespace-nowrap"
        >
          Remove
        </button>
      </div>
    </div>
  )
}

function Section({ title, subtitle, action, children }) {
  return (
    <div className="bg-surface rounded-2xl p-5">
      <div className="flex items-start justify-between gap-3 flex-wrap">
        <div className="min-w-0">
          <h2 className="font-semibold text-sm font-display m-0">{title}</h2>
          {subtitle && <p className="text-[11px] text-text-dim m-0 mt-1 mb-3 leading-relaxed">{subtitle}</p>}
        </div>
        {action}
      </div>
      <div className={subtitle ? '' : 'mt-3'}>{children}</div>
    </div>
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

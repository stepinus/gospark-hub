import { useCallback, useEffect, useState } from 'react'
import { useAuth } from '../auth'
import { deleteEngine, getEngines, installEngine, updateEngine } from '../lib/api'
import ConfirmDeleteModal from '../components/ConfirmDeleteModal'

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
        <h1 className="text-2xl font-bold tracking-tight font-display m-0">Engines</h1>
        <p className="text-sm text-text-dim m-0 mt-1">
          Model runtimes are installed once on this machine. Recipes launch on them without
          containers.
        </p>
      </div>

      {error && (
        <div className="rounded-2xl border border-error/30 bg-error-surface px-4 py-3">
          <p className="text-sm text-error m-0 leading-relaxed">{error}</p>
        </div>
      )}

      {loading && engines.length === 0 ? (
        <div className="bg-surface rounded-2xl p-5">
          <p className="text-sm text-text-dim m-0">Loading engines…</p>
        </div>
      ) : engines.length === 0 ? (
        <div className="bg-surface rounded-2xl p-5">
          <p className="text-sm text-text-dim m-0">No engines.</p>
        </div>
      ) : (
        engines.map((engine) => (
          <EngineCard
            key={engine.name}
            engine={engine}
            busy={busy?.name === engine.name ? busy.action : null}
            disabled={busy !== null}
            error={failures[engine.name] || ''}
            onInstall={() => run(engine, 'install')}
            onUpdate={() => run(engine, 'update')}
            onDelete={() => setConfirm(engine)}
          />
        ))
      )}

      {confirm && (
        <ConfirmDeleteModal
          title={`Delete engine ${displayName(confirm.name)}?`}
          busy={busy?.name === confirm.name}
          onCancel={() => setConfirm(null)}
          onConfirm={confirmDelete}
        >
          Native recipes stop launching on this engine, and the installed runtime is removed from
          this machine.
        </ConfirmDeleteModal>
      )}
    </div>
  )
}

function EngineCard({ engine, busy, disabled, error, onInstall, onUpdate, onDelete }) {
  const name = displayName(engine.name)
  // `latest` is what the release feed says; an install made offline may be
  // ahead of it, and then there is nothing to update to.
  const updateAvailable = Boolean(engine.installed && engine.latest && engine.latest !== engine.version)

  return (
    <div className="bg-surface rounded-2xl p-5">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h2 className="font-semibold text-sm font-display m-0">{name}</h2>
            {updateAvailable && (
              <span className="text-[10px] font-label font-semibold text-primary bg-primary/10 border border-primary/30 px-2 py-0.5 rounded-full whitespace-nowrap">
                Update to {engine.latest} available
              </span>
            )}
          </div>
          <p className="text-[11px] text-text-dim m-0 mt-1">
            {engine.installed ? `Version ${engine.version || 'unknown'}` : 'Not installed'}
            {engine.installed && engine.latest && !updateAvailable && ` · up to date`}
          </p>
          {engine.installed && engine.path && (
            <p className="text-[11px] text-text-dim font-mono m-0 mt-1 break-all">{engine.path}</p>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-2">
          {!engine.installed && (
            <button
              onClick={onInstall}
              disabled={disabled}
              className="btn-primary px-4 py-2 text-sm font-bold disabled:opacity-50 disabled:cursor-default"
            >
              {busy === 'install' ? '⟳ Installing…' : 'Install'}
            </button>
          )}
          {engine.installed && (
            <>
              <button
                onClick={onUpdate}
                disabled={disabled || !updateAvailable}
                className="px-3.5 py-2 rounded-xl bg-surface-high text-text border border-outline-dim text-xs font-semibold cursor-pointer hover:bg-surface-highest disabled:opacity-50 disabled:cursor-default transition-colors whitespace-nowrap"
              >
                {busy === 'update' ? '⟳ Updating…' : 'Update'}
              </button>
              <button
                onClick={onDelete}
                disabled={disabled}
                className="px-3.5 py-2 rounded-xl bg-error-surface text-error border border-error/30 text-xs font-semibold cursor-pointer hover:bg-error/15 disabled:opacity-50 disabled:cursor-default transition-colors whitespace-nowrap"
              >
                {busy === 'delete' ? '⟳ Deleting…' : 'Delete'}
              </button>
            </>
          )}
        </div>
      </div>

      {error && <p className="text-xs text-error m-0 mt-3 leading-relaxed">{error}</p>}
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

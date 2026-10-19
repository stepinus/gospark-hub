import { useEffect, useRef } from 'react'

/* Deleting a local recipe removes files, so it never happens on one click.
 *
 * The same dialog is used from Settings (`local/` list) and from the recipe
 * card, because it is the same act: the confirmation has to name the recipe
 * and the folder that is about to disappear, and nothing else. Focus starts on
 * Cancel — a stray Enter must not delete anything — while Escape, the backdrop
 * and Cancel all mean the same thing.
 */
export default function ConfirmDeleteModal({ name, slug, busy, error, onCancel, onConfirm }) {
  const cancelRef = useRef(null)

  useEffect(() => {
    cancelRef.current?.focus()
  }, [])

  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm animate-fadeIn px-4"
      onClick={onCancel}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-delete-title"
        aria-describedby="confirm-delete-text"
        onClick={(e) => e.stopPropagation()}
        className="bg-surface-high rounded-2xl p-6 w-full max-w-md shadow-2xl border border-outline-dim"
      >
        <h3 id="confirm-delete-title" className="text-lg font-bold text-text font-display m-0">
          Delete recipe {name || slug}?
        </h3>
        <p id="confirm-delete-text" className="text-sm text-text-dim mt-2 mb-0 leading-relaxed">
          The files will be deleted from{' '}
          {/* One path, broken after its slashes rather than mid-word. */}
          <span className="font-mono text-text-muted break-words">
            ~/.config/gospark/registry/<wbr />local/<wbr />{slug}
          </span>
          .
        </p>

        {error && <p className="text-xs text-error mt-3 mb-0 leading-relaxed">{error}</p>}

        <div className="flex justify-end gap-3 mt-5">
          <button
            ref={cancelRef}
            onClick={onCancel}
            disabled={busy}
            className="px-4 py-2 bg-transparent text-text-muted border border-outline-dim rounded-xl text-sm font-semibold cursor-pointer hover:text-text transition-colors disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            disabled={busy}
            className="px-5 py-2 bg-error text-white border-none rounded-xl text-sm font-bold cursor-pointer hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-default"
          >
            {busy ? 'Deleting…' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  )
}

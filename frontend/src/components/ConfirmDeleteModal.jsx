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

/* Deleting a local recipe removes files, so it never happens on one click.
 *
 * The same dialog is used from Settings (`local/` list) and from the recipe
 * card, because it is the same act: the confirmation has to name the recipe
 * and the folder that is about to disappear, and nothing else. Focus starts on
 * Cancel — a stray Enter must not delete anything — while Escape, the backdrop
 * and Cancel all mean the same thing.
 *
 * `title` and `children` let a caller that deletes something else (an engine)
 * say what is going away in its own words; without them the dialog speaks
 * about a local recipe folder, which is the common case.
 */
export default function ConfirmDeleteModal({ name, slug, busy, error, title, children, onCancel, onConfirm }) {
  return (
    <AlertDialog open onOpenChange={(open) => { if (!open) onCancel() }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title || `Delete recipe ${name || slug}?`}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="text-sm leading-relaxed text-muted-foreground">
              {children || (
                <>
                  The files will be deleted from{' '}
                  {/* One path, broken after its slashes rather than mid-word. */}
                  <span className="break-words font-mono text-xs text-foreground">
                    ~/.config/gospark/registry/<wbr />local/<wbr />{slug}
                  </span>
                  .
                </>
              )}
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>

        {error && <p className="m-0 text-xs leading-relaxed text-destructive">{error}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy} onClick={onCancel}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(e) => { e.preventDefault(); onConfirm() }}
            className="bg-destructive text-white hover:bg-destructive/90"
          >
            {busy ? 'Deleting…' : 'Delete'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

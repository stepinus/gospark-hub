import { useEffect } from 'react'
import { coverInfo, originalFor } from '../covers'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'

// "About this image": the picture, and what it is. The description travels
// with the recipe, so a recipe added later describes its own artwork.
export default function CoverInfoModal({ recipe, onClose }) {
  useEffect(() => {
    const onKey = (e) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const info = coverInfo(recipe)

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        className="max-h-[88vh] gap-3 overflow-y-auto border-border bg-card p-5 shadow-none sm:max-w-3xl"
        showCloseButton
      >
        <DialogTitle className="pr-8 font-display text-base font-semibold tracking-tight text-foreground">
          About this image
        </DialogTitle>

        {/* The original source file, never a rendered crop. `object-contain`
            plus a viewport cap scales it down to fit but never cuts it. */}
        <img
          src={originalFor(recipe)}
          alt=""
          className="max-h-[65vh] w-full rounded-lg border border-border bg-muted object-contain"
        />

        <p className="m-0 text-sm leading-relaxed text-muted-foreground">
          {info?.caption || 'No description recorded for this image.'}
        </p>

        {info?.credit && (
          <p className="m-0 text-xs leading-relaxed text-muted-foreground/70">
            {info.credit}
            {info.source && (
              <>
                {' · '}
                <a href={info.source} target="_blank" rel="noreferrer" className="text-primary hover:underline">
                  source
                </a>
              </>
            )}
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}

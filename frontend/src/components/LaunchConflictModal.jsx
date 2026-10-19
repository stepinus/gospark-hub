import { useState } from 'react'
import { useStore } from '../store'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

// Models run side by side while they fit in memory. When the next one does not
// fit, launching it has to stop some of what is up, and stopping a running
// model is not something to discover after the fact, so it is confirmed first.
export default function LaunchConflictModal() {
  const request = useStore((s) => s.launchRequest)
  const confirmLaunch = useStore((s) => s.confirmLaunch)
  const cancelLaunch = useStore((s) => s.cancelLaunch)
  const recipes = useStore((s) => s.recipes)
  const [swapping, setSwapping] = useState(false)

  if (!request) return null

  const nameOf = (slug) => recipes.find((r) => r.slug === slug)?.name || slug
  const incoming = nameOf(request.slug)
  const outgoing = request.blockers.map(nameOf)

  const confirm = async () => {
    setSwapping(true)
    await confirmLaunch()
    setSwapping(false)
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) cancelLaunch() }}>
      <DialogContent className="gap-4 border-border bg-card p-5 shadow-none sm:max-w-md">
        <DialogHeader className="gap-1">
          <DialogTitle className="font-display text-base font-semibold tracking-tight">
            Swap running model?
          </DialogTitle>
          <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
            They don&apos;t fit in memory together.
          </DialogDescription>
        </DialogHeader>

        {/* The swap, drawn rather than described — the names appear once, and
            the shape of the trade reads before the words do. */}
        <div className="flex items-center gap-3 rounded-lg border border-border bg-muted p-3">
          <Endpoint label="Stopping" name={outgoing.join(' and ')} dot="bg-muted-foreground" tone="text-muted-foreground" />
          <span aria-hidden className="shrink-0 text-base leading-none text-muted-foreground">→</span>
          <Endpoint label="Starting" name={incoming} dot="bg-primary" tone="text-foreground" />
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={cancelLaunch} disabled={swapping}>
            Cancel
          </Button>
          <Button onClick={confirm} disabled={swapping}>
            {swapping ? 'Swapping…' : 'Stop & Launch'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Endpoint({ label, name, dot, tone }) {
  return (
    <div className="min-w-0 flex-1">
      <p className="m-0 flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
        <span className={`h-1.5 w-1.5 rounded-full ${dot}`} />
        {label}
      </p>
      <p className={`m-0 mt-1 truncate text-sm font-semibold ${tone}`} title={name}>{name}</p>
    </div>
  )
}

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

// The companion to HfTokenModal, for the half of the problem a token cannot
// solve. A gated repo needs the account holding the token to have accepted
// that repo's agreement on HuggingFace, and there is no API to accept one —
// it is a real agreement, so the person has to read it. All this modal can do
// is name the repos, link them, and check again afterwards.
export default function HfAccessModal() {
  const request = useStore((s) => s.hfAccessRequest)
  // Keyed on the slug so a second app's gate opens with a clean dialog rather
  // than the previous one's error still on screen — a remount instead of an
  // effect that resets state on the way back in.
  return request ? <AccessDialog key={request.slug} request={request} /> : null
}

function AccessDialog({ request }) {
  const recheckHfAccess = useStore((s) => s.recheckHfAccess)
  const cancelHfAccess = useStore((s) => s.cancelHfAccess)
  const recipes = useStore((s) => s.recipes)
  const [error, setError] = useState('')
  const [checking, setChecking] = useState(false)

  const recipe = recipes.find((r) => r.slug === request.slug)
  const verb = request.action === 'install' ? 'install' : 'launch'
  const repos = request.repos || []

  const recheck = async () => {
    setChecking(true)
    setError('')
    const result = await recheckHfAccess()
    if (!result.ok) {
      setError(result.error || 'Access check failed')
      setChecking(false)
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) cancelHfAccess() }}>
      <DialogContent className="gap-4 border-border bg-card p-5 shadow-none sm:max-w-md">
        <DialogHeader className="gap-1.5">
          <DialogTitle className="font-display text-base font-semibold tracking-tight">
            HuggingFace Access Required
          </DialogTitle>
          <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
            {recipe?.name || request.slug} needs weights behind a terms gate. Your token is saved, but the
            account holding it has not accepted the agreement yet, so the download would fail. Open the
            {repos.length > 1 ? ' pages' : ' page'} below, accept the terms, then check again to {verb} it.
          </DialogDescription>
        </DialogHeader>

        <ul className="m-0 flex list-none flex-col gap-2 p-0">
          {repos.map((repo) => (
            <li key={repo.repo_id}>
              <a
                href={repo.url}
                target="_blank"
                rel="noreferrer"
                className="block break-all rounded-lg border border-border bg-muted px-3 py-2 font-mono text-xs text-primary transition-colors hover:bg-accent hover:underline"
              >
                {repo.repo_id}
              </a>
            </li>
          ))}
        </ul>

        {error && <p className="m-0 text-xs leading-relaxed text-destructive">{error}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={cancelHfAccess}>
            Cancel
          </Button>
          <Button onClick={recheck} disabled={checking}>
            {checking ? 'Checking...' : `Check & ${verb.charAt(0).toUpperCase() + verb.slice(1)}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

import { useEffect, useState } from 'react'
import { useStore } from '../store'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

export default function HfTokenModal() {
  const request = useStore((s) => s.hfTokenRequest)
  const resolveHfToken = useStore((s) => s.resolveHfToken)
  const cancelHfToken = useStore((s) => s.cancelHfToken)
  const recipes = useStore((s) => s.recipes)
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!request) {
      // The dialog closed: the next one opens with a clean token and error.
      // react-hooks/set-state-in-effect is pre-existing repo-wide (the same
      // error reports on this file before the shadcn migration).
      /* eslint-disable react-hooks/set-state-in-effect */
      setToken('')
      setError('')
      setSaving(false)
      /* eslint-enable react-hooks/set-state-in-effect */
    }
  }, [request])

  if (!request) return null

  const recipe = recipes.find((r) => r.slug === request.slug)
  const verb = request.action === 'install' ? 'install' : 'launch'

  const submit = async () => {
    if (!token.trim()) return
    setSaving(true)
    setError('')
    const result = await resolveHfToken(token)
    if (!result.ok) {
      setError(result.error || 'Failed to save token')
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) cancelHfToken() }}>
      <DialogContent className="gap-4 border-border bg-card p-5 shadow-none sm:max-w-md">
        <DialogHeader className="gap-1.5">
          <DialogTitle className="font-display text-base font-semibold tracking-tight">
            HuggingFace Token Required
          </DialogTitle>
          <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
            {recipe?.name || request.slug} uses gated assets on HuggingFace. Paste your access token below to {verb} it.
            You can create one at{' '}
            <a href="https://huggingface.co/settings/tokens" target="_blank" rel="noreferrer" className="text-primary hover:underline">
              huggingface.co/settings/tokens
            </a>.
          </DialogDescription>
        </DialogHeader>

        <Input
          type="password"
          placeholder="hf_..."
          value={token}
          onChange={(e) => setToken(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && submit()}
          className="h-9 font-mono text-xs"
          autoFocus
        />

        {error && <p className="m-0 text-xs leading-relaxed text-destructive">{error}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={cancelHfToken}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={!token.trim() || saving}>
            {saving ? 'Saving...' : `Save & ${verb.charAt(0).toUpperCase() + verb.slice(1)}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

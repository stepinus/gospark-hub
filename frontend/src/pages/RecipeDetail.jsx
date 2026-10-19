import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, Check, Copy, GitBranch, Globe, Info } from 'lucide-react'
import { useStore } from '../store'
import { useThemedLogo } from '../hooks/useThemedLogo'
import { formatParams, openUrl } from '../components/RecipeCard'
import { fillPlaceholders, hubOrigin } from '../lib/urls'
import { hasSecret, maskIn } from '../lib/secret'
import { useApiKey } from '../hooks/useApiKey'
import { useAuth } from '../auth'
import { backdropFor, posterFor } from '../covers'
import { formatDuration, mediaKind, speedLabel } from '../models'
import CoverInfoModal from '../components/CoverInfoModal'
import YamlEditor from '../components/YamlEditor'
import CopyField from '../components/CopyField'
import { deleteLocalRecipe, forkRecipe } from '../lib/api'
import { Field, PageHeader, SectionTitle } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

const DETAIL_TABS = [
  { id: 'details', label: 'Overview' },
  { id: 'logs', label: 'Logs' },
]

// A slug becomes a directory name in ~/.config/gospark/registry/local/, so it
// is kept to what is safe there and to a URL: 2–63 characters, lowercase
// letters, digits and dashes, never starting with a dash.
const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}$/

function suggestCopySlug(slug) {
  const base = `${slug || 'recipe'}-copy`
  const trimmed = base.length > 63 ? `${base.slice(0, 58)}-copy` : base
  return SLUG_PATTERN.test(trimmed) ? trimmed : 'my-recipe'
}

// Remount on slug change so per-recipe local state (active tab, logo
// fallback, …) doesn't leak from one app's page to the next.
export default function RecipeDetail() {
  const { slug } = useParams()
  return <RecipeDetailPage key={slug} slug={slug} />
}

function RecipeDetailPage({ slug }) {
  const navigate = useNavigate()
  const recipes = useStore((s) => s.recipes)
  // "Back" walks browser history when there is somewhere to go back to,
  // so it lands where the user came from (Store or Running).
  const goBack = () => {
    if (window.history.length > 1) navigate(-1)
    else navigate('/')
  }
  const installing = useStore((s) => s.installing)
  const updating = useStore((s) => s.updating)
  const removing = useStore((s) => s.removing)
  const installRecipe = useStore((s) => s.installRecipe)
  const ensureHfAccess = useStore((s) => s.ensureHfAccess)
  const updateRecipe = useStore((s) => s.updateRecipe)
  const launchOrSwap = useStore((s) => s.launchOrSwap)
  const stopRecipe = useStore((s) => s.stopRecipe)
  const removeRecipe = useStore((s) => s.removeRecipe)
  const purging = useStore((s) => s.purging)
  const purgeRecipe = useStore((s) => s.purgeRecipe)
  const buildLogs = useStore((s) => s.buildLogs)
  const containerLogs = useStore((s) => s.containerLogs)
  const lastInstallFailed = useStore((s) => s.lastInstallFailed)
  const connectLogs = useStore((s) => s.connectLogs)
  const disconnectLogs = useStore((s) => s.disconnectLogs)
  const fetchRecipes = useStore((s) => s.fetchRecipes)

  const recipe = recipes.find((r) => r.slug === slug)
  const scrollRef = useRef(null)
  const previousRecipeRef = useRef(null)
  const previousPreferLogsRef = useRef(false)
  const [logoFailed, setLogoFailed] = useState(false)
  const [posterFailed, setPosterFailed] = useState(false)
  const [backdropFailed, setBackdropFailed] = useState(false)
  const [showCoverInfo, setShowCoverInfo] = useState(false)
  const [launching, setLaunching] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [activeTab, setActiveTab] = useState('details')
  const [showHfModal, setShowHfModal] = useState(false)
  const [hfToken, setHfToken] = useState('')
  const [hfSaving, setHfSaving] = useState(false)
  const [hfError, setHfError] = useState('')
  // “Create copy” and “Delete”: both act on ~/.config/gospark/registry/local/,
  // and both report the daemon's own refusal (a taken slug, an app still
  // installed) in the dialog that asked, so the page never goes blank.
  const [showFork, setShowFork] = useState(false)
  const [showDelete, setShowDelete] = useState(false)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteError, setDeleteError] = useState('')
  // Must run before any early return — hooks can't be conditional, and on a
  // deep link this component first renders with the catalog still empty.
  const logoUrl = useThemedLogo(recipe?.logo)

  const isBuilding = !!installing[recipe?.slug]
  const isUpdating = !!updating[recipe?.slug]
  const isBusy = isBuilding || isUpdating

  const installFailed = !!lastInstallFailed[recipe?.slug]

  useEffect(() => {
    if ((recipe?.running || recipe?.starting) && !installFailed) {
      connectLogs(recipe.slug)
    } else {
      disconnectLogs()
    }
    return () => disconnectLogs()
  }, [recipe?.running, recipe?.starting, recipe?.slug, installFailed, connectLogs, disconnectLogs])

  useEffect(() => {
    if (!recipe) return

    const preferLogs = isBuilding || recipe.starting
    const recipeChanged = previousRecipeRef.current !== recipe.slug

    if (recipeChanged) {
      setActiveTab(preferLogs ? 'logs' : 'details')
      previousRecipeRef.current = recipe.slug
      previousPreferLogsRef.current = preferLogs
      return
    }

    if (preferLogs && !previousPreferLogsRef.current) {
      setActiveTab('logs')
    }

    previousPreferLogsRef.current = preferLogs
  }, [recipe, isBuilding])

  // Deep link / hard refresh: the catalog hasn't arrived yet, so don't
  // claim the recipe is missing until we actually have the list.
  if (!recipe && recipes.length === 0) {
    return (
      <div className="page">
        <p className="m-0 text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }

  if (!recipe) {
    return (
      <div className="page flex flex-col gap-4">
        <div>
          <Button
            variant="ghost"
            size="sm"
            onClick={goBack}
            className="-ml-2 text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="size-4" />
            Back
          </Button>
        </div>
        <p className="m-0 text-sm text-muted-foreground">Recipe not found.</p>
      </div>
    )
  }

  const isRemoving = !!removing[recipe.slug]
  // A native recipe is a launch config for an engine installed on the host:
  // there is no image to build and no container to uninstall. `kind` is only
  // present on native recipes (the daemon keeps container JSON unchanged).
  const isNative = recipe.kind === 'native'
  const native = recipe.native || {}
  // A model can need its own patched engine build (the model repository's patch
  // series). The daemon extracts it into the engine home; until that folder is
  // there, launching is refused.
  const engineBuild = recipe.engine_build || null
  const isReady = recipe.ready
  const cLogs = containerLogs[recipe.slug] || []
  const bLogs = buildLogs[recipe.slug] || []
  // Pin to build logs while installing/updating, or when the last install
  // failed (so the user can read the error instead of seeing it flash to
  // "Container not running").
  const logLines = isBusy || installFailed ? bLogs : cLogs

  const handleRemove = () => {
    if (window.confirm(`Uninstall ${recipe.name}? This removes containers, images, and volumes.`)) {
      removeRecipe(recipe.slug)
    }
  }

  const handleLaunch = async () => {
    if (recipe.requires_hf_token) {
      const res = await fetch('/api/system/hf-token')
      const { has_token } = await res.json()
      if (!has_token) {
        setShowHfModal(true)
        return
      }
    }
    if (!(await ensureHfAccess(recipe.slug, 'launch'))) return
    setLaunching(true)
    await launchOrSwap(recipe.slug)
    setLaunching(false)
  }

  const handleHfSubmit = async () => {
    if (!hfToken.trim()) return
    setHfSaving(true)
    setHfError('')
    try {
      const res = await fetch('/api/system/hf-token', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: hfToken.trim() }),
      })
      if (!res.ok) throw new Error('Failed to save token')
      setShowHfModal(false)
      setHfToken('')
      if (!(await ensureHfAccess(recipe.slug, 'launch'))) return
      setLaunching(true)
      await launchOrSwap(recipe.slug)
      setLaunching(false)
    } catch {
      setHfError('Failed to save token. Please try again.')
    } finally {
      setHfSaving(false)
    }
  }

  const handleStop = async () => {
    setStopping(true)
    await stopRecipe(recipe.slug)
    setStopping(false)
  }

  // A copy lands in local/<newSlug>; the catalog is refetched before navigating
  // so the new page has something to render.
  const handleForked = async (newSlug) => {
    setShowFork(false)
    await fetchRecipes()
    navigate(`/app/${encodeURIComponent(newSlug)}`)
  }

  const handleDeleteLocal = async () => {
    setDeleteBusy(true)
    setDeleteError('')
    const res = await deleteLocalRecipe(recipe.slug)
    setDeleteBusy(false)
    if (!res.ok) {
      setDeleteError(res.error)
      return
    }
    setShowDelete(false)
    await fetchRecipes()
    navigate('/')
  }

  const cancelInstall = () => {
    if (window.confirm(`Cancel install for ${recipe.name}? This will stop the download.`)) {
      removeRecipe(recipe.slug)
    }
  }

  const recipeCategories = Array.isArray(recipe.categories) && recipe.categories.length > 0
    ? recipe.categories
    : [recipe.category]

  // Recipes cloned from a source are overwritten by the next sync; only the
  // copies in local/ belong to this Hub. A daemon that predates the field
  // simply reports false, which is the truthful default.
  const isLocal = recipe.local === true

  // The lifecycle rows below carry exactly the conditions the old action row
  // used: "can launch" is that row, "up or starting" is its mirror image.
  const canLaunch = (isNative || recipe.installed) && !recipe.running && !recipe.starting && !isBusy && !isRemoving
  const isUpOrStarting = recipe.running || ((isNative || recipe.installed) && recipe.starting)
  const canUpdate = !isNative && canLaunch
  const canUninstall = !isNative && (canLaunch || isUpOrStarting)
  const canRemoveEngine = isNative && !!engineBuild?.ready && !recipe.running && !isBusy && !isRemoving
  const canCancelInstall = isBuilding && !isNative
  const hasControls = canLaunch || isUpOrStarting || canUpdate || canUninstall || canRemoveEngine || canCancelInstall

  return (
    <div className="page flex flex-col gap-6">
      <PageHeader
        eyebrow={recipeCategories.join(' · ')}
        title={recipe.name}
        actions={
          <>
            {isBuilding && !isNative && (
              <Badge variant="outline" className="h-7 gap-1.5 border-primary/30 bg-primary/10 px-2.5 text-primary">
                <span className="font-mono">⟳</span>
                Building
              </Badge>
            )}
            {isUpdating && (
              <Badge variant="outline" className="h-7 gap-1.5 border-primary/30 bg-primary/10 px-2.5 text-primary">
                <span className="font-mono">⟳</span>
                Updating
              </Badge>
            )}
            {isRemoving && (
              <Badge variant="outline" className="h-7 gap-1.5 border-destructive/30 bg-destructive/10 px-2.5 text-destructive">
                <span className="font-mono">⟳</span>
                Uninstalling
              </Badge>
            )}
            {!isNative && !recipe.installed && !recipe.starting && !isBusy && !isRemoving && (
              <Button onClick={() => installRecipe(recipe.slug)}>Install</Button>
            )}
            {isNative && engineBuild && !engineBuild.ready && !isBusy && !isRemoving && (
              <Button onClick={() => installRecipe(recipe.slug)}>Build engine</Button>
            )}
            {isUpOrStarting && isReady && (
              <Button asChild>
                <a href={openUrl(recipe)} target="_blank" rel="noreferrer">
                  Open ↗
                </a>
              </Button>
            )}
            {/* Every recipe can be copied; only a local one can be deleted. */}
            <Button variant="outline" onClick={() => setShowFork(true)}>
              Create copy
            </Button>
          </>
        }
      >
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-3">
          {logoUrl && !logoFailed ? (
            <img
              src={logoUrl}
              alt={recipe.name}
              onError={() => setLogoFailed(true)}
              className="h-10 w-10 shrink-0 rounded-lg border border-border bg-muted object-contain p-1.5"
            />
          ) : (
            <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border border-border bg-muted text-lg">
              {recipe.icon || '◻'}
            </div>
          )}

          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            {isBuilding && <StatusBadge tone="primary" pulse>Building...</StatusBadge>}
            {isUpdating && <StatusBadge tone="primary" pulse>Updating...</StatusBadge>}
            {!isBusy && recipe.running && isReady && <StatusBadge tone="success">Running</StatusBadge>}
            {!isBusy && recipe.starting && <StatusBadge tone="warning" pulse>Starting...</StatusBadge>}
            {!isBusy && !recipe.running && !recipe.starting && recipe.installed && <StatusBadge tone="dim">Stopped</StatusBadge>}
            <Badge variant="outline" className="text-[11px] font-normal">
              {isLocal ? 'Local' : 'From registry'}
            </Badge>
            {isNative && (
              <Badge variant="secondary" className="text-[11px] font-normal">
                Native
              </Badge>
            )}
          </div>

          {/* This app's cover art, both halves of it: the wide backdrop and the
              poster, so every image in the catalog is visible somewhere and not
              just the handful the hero happens to rotate. The dialog explains
              where the picture came from. */}
          <div className="ml-auto flex items-center gap-2">
            {!backdropFailed && (
              <img
                src={backdropFor(recipe)}
                alt=""
                onError={() => setBackdropFailed(true)}
                className="h-12 w-20 rounded-md border border-border bg-muted object-cover"
              />
            )}
            {!posterFailed && (
              <img
                src={posterFor(recipe)}
                alt={`${recipe.name} cover`}
                onError={() => setPosterFailed(true)}
                className="h-12 w-8 rounded-md border border-border bg-muted object-cover"
              />
            )}
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShowCoverInfo(true)}
              title="What is this picture?"
              className="text-muted-foreground hover:text-foreground"
            >
              <Info className="size-3.5" />
              About this image
            </Button>
          </div>
        </div>

        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
          <span className="truncate">{recipe.author}</span>
          {recipe.quantization && (
            <span className="font-mono text-xs text-foreground">{recipe.quantization}</span>
          )}
          {recipe.tokens_per_second != null && (
            <span className="font-mono text-xs tabular-nums text-foreground">{speedLabel(recipe)}</span>
          )}
        </div>
      </PageHeader>

      {recipe.error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3">
          <div className="flex items-start gap-3">
            <span className="text-base leading-6 text-destructive">⚠</span>
            <div className="min-w-0">
              <p className="m-0 text-sm font-semibold text-destructive">Couldn't start</p>
              <p className="m-0 mt-0.5 text-sm leading-relaxed text-muted-foreground">{recipe.error}</p>
            </div>
          </div>
        </div>
      )}

      {/* Launch and teardown live outside the tabs: a build switches the page to
          Logs on its own, and Cancel has to stay reachable while that happens. */}
      {hasControls && (
        <Card className="gap-0 p-5 shadow-none">
          <SectionTitle hint="one at a time">Controls</SectionTitle>
          <div className="flex flex-col">
            {canLaunch && (
              <ControlRow
                label="Launch"
                hint="Start this app on the Spark. Launching another app stops the one that is running."
              >
                <Button
                  disabled={launching || isRemoving || (engineBuild && !engineBuild.ready)}
                  onClick={handleLaunch}
                >
                  {launching ? '...' : '▶ Launch'}
                </Button>
              </ControlRow>
            )}
            {isUpOrStarting && (
              <ControlRow label="Stop" hint="Shut the app down and free the memory it is holding.">
                <Button variant="outline" disabled={stopping} onClick={handleStop}>
                  {stopping ? '...' : '■ Stop'}
                </Button>
              </ControlRow>
            )}
            {canUpdate && (
              <ControlRow label="Update" hint="Pull the newest image and rebuild this app's container.">
                <Button
                  variant="outline"
                  disabled={launching || isRemoving}
                  onClick={() => updateRecipe(recipe.slug)}
                >
                  ↻ Update
                </Button>
              </ControlRow>
            )}
            {canUninstall && (
              <ControlRow label="Uninstall" hint="Remove the container, its image and its volumes from this machine.">
                <Button variant="destructive" disabled={isRemoving} onClick={handleRemove}>
                  {isRemoving ? '...' : 'Uninstall'}
                </Button>
              </ControlRow>
            )}
            {canRemoveEngine && (
              <ControlRow label="Remove engine" hint="Delete the engine build this recipe needs. The engine itself stays installed.">
                <Button variant="destructive" disabled={isRemoving} onClick={handleRemove}>
                  Remove engine
                </Button>
              </ControlRow>
            )}
            {canCancelInstall && (
              <ControlRow label="Cancel install" hint="Stop the download. What has already arrived is kept.">
                <Button variant="destructive" onClick={cancelInstall}>
                  Cancel
                </Button>
              </ControlRow>
            )}
          </div>
        </Card>
      )}

      <Tabs value={activeTab} onValueChange={setActiveTab} className="gap-4">
        <TabsList variant="line">
          {DETAIL_TABS.map((tab) => (
            <TabsTrigger key={tab.id} value={tab.id} className="px-3 text-xs font-medium">
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="details" className="flex flex-col gap-6 outline-none">
          {/* Every fact the recipe ships, as a definition list. */}
          <Card className="gap-0 p-5 shadow-none">
            <SectionTitle>Specs</SectionTitle>
            <div className="grid gap-x-8 sm:grid-cols-2">
              {/* A native recipe names its engine in `native`; the model's own
                  `engine` field would print a second row titled Engine. */}
              {recipe.engine && !isNative && (
                <Field label="Engine" mono value={recipe.engine} className="border-b border-border" />
              )}
              {recipe.params_b != null && (
                <Field
                  label={recipe.arch === 'moe' ? 'MoE' : 'Dense'}
                  mono
                  value={formatParams(recipe)}
                  className="border-b border-border"
                />
              )}
              {recipe.media_capabilities?.length > 0 && (
                <SpecRow
                  label="Supports"
                  value={recipe.media_capabilities.map((c) => MEDIA_LABELS[c] || c).join(' · ')}
                />
              )}
              {recipe.quantization && (
                <Field label="Precision" mono value={recipe.quantization} className="border-b border-border" />
              )}
              {recipe.weights_gb != null && (
                <Field label="Weights" mono value={`${recipe.weights_gb} GB`} className="border-b border-border" />
              )}
              <Field
                label="Memory"
                mono
                value={`${recipe.requirements?.min_memory_gb ?? 8}–${recipe.requirements?.recommended_memory_gb ?? recipe.requirements?.min_memory_gb ?? 8} GB`}
                className="border-b border-border"
              />
              {/* A native recipe has no image and no container disk: what it
                  needs is the engine, the checkpoint and a host port. */}
              {isNative ? (
                <>
                  {native.engine && (
                    <Field label="Engine" mono value={native.engine} className="border-b border-border" />
                  )}
                  {native.model && (
                    <Field label="Model" mono value={native.model} className="border-b border-border" />
                  )}
                  {native.port != null && (
                    <Field label="Port" mono value={String(native.port)} className="border-b border-border" />
                  )}
                  {engineBuild && (
                    <Field
                      label="Engine build"
                      mono
                      value={engineBuild.ready ? 'built' : 'not built'}
                      className="border-b border-border"
                    />
                  )}
                </>
              ) : (
                <Field
                  label="Disk"
                  mono
                  value={`${recipe.requirements?.disk_gb ?? 10} GB`}
                  className="border-b border-border"
                />
              )}
              {recipe.tokens_per_second != null && (
                <Field label="Speed" mono value={speedLabel(recipe)} className="border-b border-border" />
              )}
              {recipe.render_seconds != null && (
                <SpecRow
                  label="Time"
                  value={`${formatDuration(recipe.render_seconds)} per ${mediaKind(recipe) || 'result'}${recipe.render_workload ? ` · ${recipe.render_workload}` : ''}`}
                />
              )}
              {recipe.load_seconds != null && (
                <Field
                  label="Ready in"
                  mono
                  value={formatDuration(recipe.load_seconds)}
                  className="border-b border-border"
                />
              )}
              {(recipe.arena || []).map((a) => (
                <SpecRow key={a.board} label={`Arena · ${a.task.replace(/-/g, ' ')}`}>
                  <a
                    href={a.url}
                    target="_blank"
                    rel="noreferrer"
                    className="text-primary hover:underline"
                  >
                    {`${Math.round(a.elo)} Elo${a.base ? ` (${a.entry}'s)` : ''} · ${a.board} · ${a.read}`}
                  </a>
                </SpecRow>
              ))}
            </div>
          </Card>

          <div className={`grid grid-cols-1 gap-6 ${isNative ? '' : 'xl:grid-cols-[minmax(0,50rem)_minmax(24rem,1fr)]'}`}>
            <AboutTab recipe={recipe} purging={purging} purgeRecipe={purgeRecipe} isBuilding={isBusy} />

            {/* A native recipe has no docker-compose.yml to edit; the engine
                and its flags live in recipe.yaml. */}
            {!isNative && (
              <div className="min-w-0">
                <ComposeEditor slug={recipe.slug} local={isLocal} />
              </div>
            )}
          </div>

          {isLocal && (
            <Card className="gap-0 p-5 shadow-none">
              <SectionTitle>Danger zone</SectionTitle>
              <div className="flex flex-wrap items-center justify-between gap-4">
                <div className="min-w-0">
                  <p className="m-0 text-sm font-medium text-foreground">Delete this copy</p>
                  <p className="m-0 mt-1 max-w-xl text-xs leading-relaxed text-muted-foreground">
                    The folder is removed from{' '}
                    <span className="font-mono text-foreground">~/.config/gospark/registry/local/</span>{' '}
                    and this Hub stops listing it. Recipes from a registry source cannot be deleted this way.
                  </p>
                </div>
                <Button
                  variant="destructive"
                  onClick={() => { setDeleteError(''); setShowDelete(true) }}
                >
                  Delete
                </Button>
              </div>
            </Card>
          )}
        </TabsContent>

        <TabsContent value="logs" className="outline-none">
          <div className="h-[70vh] min-h-[420px] overflow-hidden rounded-lg border border-border">
            <TerminalPanel
              lines={logLines}
              isBuilding={isBusy}
              isUpdating={isUpdating}
              isRunning={recipe.running || recipe.starting}
              isReady={isReady}
              hasLogs={cLogs.length > 0}
              scrollRef={scrollRef}
              wide
            />
          </div>
        </TabsContent>
      </Tabs>

      {showHfModal && (
        <Dialog
          open
          onOpenChange={(open) => { if (!open) { setShowHfModal(false); setHfToken(''); setHfError('') } }}
        >
          <DialogContent className="gap-3 border-border bg-card p-5 shadow-none sm:max-w-md">
            <DialogHeader>
              <DialogTitle className="font-display text-base font-semibold tracking-tight">
                HuggingFace Token Required
              </DialogTitle>
              <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
                This model uses gated assets on HuggingFace. Paste your access token below to continue.
                You can create one at{' '}
                <a href="https://huggingface.co/settings/tokens" target="_blank" rel="noreferrer" className="text-primary hover:underline">
                  huggingface.co/settings/tokens
                </a>.
              </DialogDescription>
            </DialogHeader>
            <Input
              type="password"
              placeholder="hf_..."
              value={hfToken}
              onChange={(e) => setHfToken(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleHfSubmit()}
              className="h-9 font-mono text-xs"
              autoFocus
            />
            {hfError && <p className="m-0 text-xs text-destructive">{hfError}</p>}
            <DialogFooter>
              <Button
                variant="outline"
                onClick={() => { setShowHfModal(false); setHfToken(''); setHfError('') }}
              >
                Cancel
              </Button>
              <Button onClick={handleHfSubmit} disabled={!hfToken.trim() || hfSaving}>
                {hfSaving ? 'Saving...' : 'Save & Launch'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

      {showCoverInfo && (
        <CoverInfoModal recipe={recipe} onClose={() => setShowCoverInfo(false)} />
      )}

      {showFork && (
        <ForkRecipeModal
          slug={recipe.slug}
          onClose={() => setShowFork(false)}
          onDone={handleForked}
        />
      )}

      {showDelete && (
        <AlertDialog
          open
          onOpenChange={(open) => { if (!open) { setShowDelete(false); setDeleteError('') } }}
        >
          <AlertDialogContent className="border-border bg-card shadow-none sm:max-w-md">
            <AlertDialogHeader>
              <AlertDialogTitle className="font-display text-base font-semibold tracking-tight">
                {`Delete recipe ${recipe.name}?`}
              </AlertDialogTitle>
              <AlertDialogDescription className="text-sm leading-relaxed text-muted-foreground">
                {isNative ? (
                  `This copy of the recipe is deleted from ~/.config/gospark/registry/local/${recipe.slug}. The engine and the checkpoint stay where they are.`
                ) : (
                  <>
                    The files will be deleted from{' '}
                    <span className="break-words font-mono text-foreground">
                      ~/.config/gospark/registry/local/{recipe.slug}
                    </span>
                    .
                  </>
                )}
              </AlertDialogDescription>
            </AlertDialogHeader>
            {deleteError && <p className="m-0 text-xs leading-relaxed text-destructive">{deleteError}</p>}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={deleteBusy} onClick={() => { setShowDelete(false); setDeleteError('') }}>
                Cancel
              </AlertDialogCancel>
              {/* preventDefault: a failing delete (a 409 from the daemon) has to
                  stay readable in this dialog, so the action must not close it. */}
              <AlertDialogAction
                variant="destructive"
                disabled={deleteBusy}
                onClick={(e) => { e.preventDefault(); handleDeleteLocal() }}
              >
                {deleteBusy ? 'Deleting…' : 'Delete'}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  )
}

// One labelled lifecycle row: what it does on the left, the control on the
// right, the same shape as the toggles on Engines.
function ControlRow({ label, hint, children }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border py-3 first:pt-0 last:border-b-0 last:pb-0">
      <div className="min-w-0">
        <p className="m-0 text-xs font-semibold text-foreground">{label}</p>
        <p className="m-0 mt-0.5 max-w-lg text-[11px] leading-relaxed text-muted-foreground">{hint}</p>
      </div>
      <div className="flex shrink-0 items-center gap-2">{children}</div>
    </div>
  )
}

// State is a dot and a word, never a painted panel.
function StatusBadge({ tone, pulse, children }) {
  const tones = {
    primary: 'border-primary/30 bg-primary/10 text-primary',
    success: 'border-success/30 bg-success/10 text-success',
    warning: 'border-warning/30 bg-warning/10 text-warning',
    dim: 'border-border bg-muted text-muted-foreground',
  }
  const dots = {
    primary: 'bg-primary',
    success: 'bg-success',
    warning: 'bg-warning',
    dim: 'bg-muted-foreground',
  }
  return (
    <Badge variant="outline" className={`gap-1.5 text-[11px] font-normal ${tones[tone] || tones.dim}`}>
      <span className={`h-1.5 w-1.5 rounded-full ${dots[tone] || dots.dim} ${pulse ? 'animate-pulse' : ''}`} />
      {children}
    </Badge>
  )
}

// The four workloads behind the speed range, always on screen. A range invites
// exactly one question — where did each end come from — and answering it behind
// a click means the number gets believed instead of checked. It is four rows.
const WORKLOAD_NOTES = {
  code: 'Write a quicksort with comments — fresh generation, nothing to copy.',
  explainer: 'TCP vs UDP — fresh prose, the least predictable output here.',
  prose: 'A Kyoto itinerary — fresh prose.',
  'code-edit': 'Re-emit a 45-class module with one method added to every class, thinking off. Most of the output is already in the prompt.',
}
// Ordered by how predictable the output is, least to most — which is also
// slowest to fastest. Read top to bottom, the four rows are the argument for
// why this build reports a range rather than a number.
const WORKLOAD_ORDER = ['prose', 'explainer', 'code', 'code-edit']

function SpeedDetail({ recipe }) {
  const rows = WORKLOAD_ORDER.filter((w) => recipe.benchmarks?.[w] != null)
  if (rows.length === 0) return null

  return (
    <div className="border-t border-border pt-4">
      <div className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        How this speed was measured
      </div>
      <div className="mt-3 flex flex-col gap-2">
        {rows.map((w) => (
          <div key={w} className="flex items-baseline gap-3">
            <span className="w-14 shrink-0 text-right font-mono text-[13px] font-semibold tabular-nums text-primary">
              {recipe.benchmarks[w]}
            </span>
            <span className="min-w-0">
              <span className="text-[11px] font-semibold uppercase tracking-[0.08em] text-foreground">{w}</span>
              <span className="block text-[11px] leading-5 text-muted-foreground">{WORKLOAD_NOTES[w]}</span>
            </span>
          </div>
        ))}
        <p className="m-0 pt-1 text-[11px] leading-5 text-muted-foreground">
          The headline number averages the three fresh-generation prompts, with thinking
          left on as this model ships it. Where a build drafts speculatively, code-edit is
          reported alongside it as the top of the range: the drafter's proposals are nearly
          always right when the answer is already in the prompt, and the same server then
          verifies several tokens per forward pass. That run sends
          <code className="mx-1 rounded bg-muted px-1 font-mono text-[10px] text-foreground">enable_thinking: false</code>
          — reasoning tokens are invented rather than copied, so a drafter cannot predict
          them, and leaving thinking on drags the peak back down to roughly the headline
          number. Run the script below to reproduce both on your own Spark.
        </p>
      </div>
    </div>
  )
}

// A definition-list row for the values Field would truncate: capabilities,
// durations and arena lines are sentences, and a sentence that ends in an
// ellipsis is a fact the page failed to state.
function SpecRow({ label, value, children }) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b border-border py-2">
      <span className="shrink-0 text-[11px] uppercase tracking-[0.1em] text-muted-foreground">{label}</span>
      <span className="min-w-0 text-right text-sm leading-relaxed text-foreground">{children ?? value}</span>
    </div>
  )
}

function RelatedRecipeCard({ recipe, onSelect }) {
  const logoUrl = useThemedLogo(recipe.logo)
  const [logoFailed, setLogoFailed] = useState(false)

  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-border bg-muted/40 p-3">
      <div className="flex min-w-0 items-center gap-3">
        {logoUrl && !logoFailed ? (
          <img
            src={logoUrl}
            alt={recipe.name}
            className="h-10 w-10 shrink-0 rounded-md bg-muted object-contain p-1.5"
            onError={() => setLogoFailed(true)}
          />
        ) : (
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-muted text-base">
            {recipe.icon || '◻'}
          </div>
        )}
        <div className="min-w-0">
          <div className="text-sm font-medium text-foreground">{recipe.name}</div>
          <div className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{recipe.description}</div>
        </div>
      </div>
      <Button variant="outline" size="sm" onClick={() => onSelect(recipe.slug)} className="shrink-0">
        View
      </Button>
    </div>
  )
}

function AboutTab({ recipe, purging, purgeRecipe, isBuilding }) {
  const recipes = useStore((s) => s.recipes)
  const selectRecipe = useStore((s) => s.selectRecipe)
  // /v1 goes through the Hub, so a call needs the caller's own Hub key. The
  // page never prints it -- it is masked behind a reveal on Account for a
  // reason -- so the snippets reference the variable `sah env` exports and
  // the field says where to find it.
  const authEnabled = useAuth((s) => s.authEnabled)
  const keyToken = authEnabled ? '$OPENAI_API_KEY' : 'not-needed'
  // The benchmark snippet is meant to be copied and run with nothing else set
  // up, so it carries the real key rather than a variable the reader has to
  // export first. The key is rendered blurred (see BenchmarkBlock): it is in
  // the DOM, so Copy and text selection both take the working value, but it is
  // not readable on screen or in a screenshot of this page.
  const benchKey = useApiKey()
  const benchKeyLiteral = benchKey || (authEnabled ? '$OPENAI_API_KEY' : 'not-needed')
  const apiKeyHint = authEnabled
    ? 'Your Hub API key — Account ▸ Your API key'
    : 'not-needed (this Hub has authentication off)'
  const officialUrl = recipe.website || ''
  const sourceUrl = recipe.upstream || recipe.fork || ''
  const relatedRecipes = (recipe.depends_on || [])
    .map((slug) => recipes.find((item) => item.slug === slug))
    .filter(Boolean)
  // The Hub forwards /v1 to whichever model a request names, so the address on
  // this page keeps working — but only for engines that serve /v1 at all.
  const forwardsNote = recipe.tags?.includes('vllm') || recipe.tags?.includes('sglang') || recipe.tags?.includes('exllamav3') || recipe.tags?.includes('atlas') || recipe.tags?.includes('tensorfold')
  const canBenchmark = recipe.tags?.includes('vllm') || recipe.tags?.includes('sglang') || recipe.tags?.includes('llama-cpp') || recipe.tags?.includes('exllamav3') || recipe.tags?.includes('atlas') || recipe.tags?.includes('tensorfold')
  const hasSpeedRows = WORKLOAD_ORDER.some((w) => recipe.benchmarks?.[w] != null)

  return (
    <div className="flex min-w-0 flex-col gap-6">
      {/* ─── Overview ─── */}
      <Card className="gap-0 p-5 shadow-none">
        <SectionTitle>Overview</SectionTitle>
        <p className="m-0 text-sm leading-relaxed text-foreground">{recipe.description}</p>

        {(officialUrl || sourceUrl) && (
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
            {officialUrl && (
              <Button asChild variant="link" size="sm" className="h-auto p-0 text-xs">
                <a href={officialUrl} target="_blank" rel="noreferrer">
                  <Globe className="size-3.5" />
                  Website
                </a>
              </Button>
            )}
            {sourceUrl && (
              <Button asChild variant="link" size="sm" className="h-auto p-0 text-xs text-muted-foreground hover:text-foreground">
                <a href={sourceUrl} target="_blank" rel="noreferrer">
                  <GitBranch className="size-3.5" />
                  Source
                </a>
              </Button>
            )}
          </div>
        )}

        {recipe.tags.length > 0 && (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {recipe.tags.map((t) => (
              <Badge key={t} variant="outline" className="text-[11px] font-normal text-muted-foreground">
                {t}
              </Badge>
            ))}
          </div>
        )}

        {relatedRecipes.length > 0 && (
          <div className="mt-5 border-t border-border pt-4">
            <SectionTitle>Suggested Add-On</SectionTitle>
            <p className="m-0 mb-3 text-xs leading-relaxed text-muted-foreground">
              For quick local testing, install a shared model runtime and connect this app to its default endpoint.
            </p>
            <div className="flex flex-col gap-2">
              {relatedRecipes.map((item) => (
                <RelatedRecipeCard key={item.slug} recipe={item} onSelect={selectRecipe} />
              ))}
            </div>
          </div>
        )}

        {recipe.runtime_env_path && (
          <div className="mt-5 border-t border-border pt-4">
            <SectionTitle>Advanced Configuration</SectionTitle>
            <p className="m-0 mb-3 text-xs leading-relaxed text-muted-foreground">
              This app creates a local runtime config file on first install. Most users should leave it alone.
              If you know what you are doing, you can edit it manually and relaunch the app.
            </p>
            {/* CopyField is the shared copy-row: the value is what lands in the
                clipboard, which is the point of printing a path. */}
            <CopyField label="Runtime env file" value={recipe.runtime_env_path} />
          </div>
        )}
      </Card>

      {/* ─── API integration ─── */}
      {recipe.integration && (
        <Card className="gap-0 p-5 shadow-none">
          <SectionTitle>API Integration</SectionTitle>
          {forwardsNote && (
            <p className="m-0 mb-3 text-[11px] leading-relaxed text-muted-foreground">
              The Hub forwards /v1 to the running model a request names, or to the
              largest one up, so this address keeps working from wherever you reached
              this page.
            </p>
          )}
          <div className="flex flex-col gap-3">
            <CopyField label="API URL" value={fillPlaceholders(recipe.integration.api_url)} />
            <CopyField label="Model ID" value={recipe.integration.model_id} />
            <CopyField label="API Key" value={apiKeyHint} />
            {recipe.capabilities?.length > 0 && <CapabilityField capabilities={recipe.capabilities} />}
            {recipe.integration.max_context && (
              <CopyField label="Max Context" value={recipe.integration.max_context} />
            )}
            {recipe.integration.max_output_tokens && (
              <CopyField label="Max Output" value={recipe.integration.max_output_tokens} />
            )}
            {recipe.integration.curl_example && (
              <CodeBlock
                label="curl example"
                value={fillPlaceholders(recipe.integration.curl_example, { apiKey: keyToken })}
              />
            )}
          </div>
        </Card>
      )}

      {/* ─── Benchmarks ─── */}
      {recipe.integration && (hasSpeedRows || canBenchmark) && (
        <Card className="gap-0 p-5 shadow-none">
          <SectionTitle>Benchmarks</SectionTitle>
          <div className="flex flex-col gap-4 pt-1">
            <SpeedDetail recipe={recipe} />
            {canBenchmark && (
              <BenchmarkBlock
                secret={benchKey}
                value={`python3 - <<'EOF'
import os, time, urllib.request, urllib.error, json
# The Hub's own endpoint, exactly as this page was reached, so the script runs
# from any machine that can open the Hub.
HOST = "${hubOrigin()}"
KEY = "${benchKeyLiteral}"  # your own Hub key, already filled in
MODEL = "${recipe.integration.model_id}"
PROMPTS = [
    ("code", "Write a quicksort implementation in Python with comments explaining each step."),
    ("explainer", "Explain the differences between TCP and UDP, and when each is preferred."),
    ("prose", "Plan a 3-day cultural travel itinerary for Kyoto, Japan in autumn."),
]
# Second workload: re-emit a file with a small change. Most of the output is
# already in the prompt, so a speculative drafter's proposals are nearly always
# right and it verifies many tokens per forward pass. Builds without a drafter
# score about the same here as above; builds with one score far higher, which is
# the whole reason both numbers are reported.
FENCE = chr(96) * 3
def source(n=45):
    src = 'from dataclasses import dataclass, field\\n'
    for i in range(1, n + 1):
        src += ('\\n@dataclass\\nclass Item{0}:\\n    sku: str\\n    quantity: int = 0\\n'
                '    price_cents: int = 0\\n    tags: list[str] = field(default_factory=list)\\n\\n'
                '    def restock(self, n: int) -> None:\\n        if n < 0:\\n'
                '            raise ValueError("n must be non-negative")\\n        self.quantity += n\\n\\n'
                '    def total_value(self) -> int:\\n        return self.quantity * self.price_cents\\n').format(i)
    return src
EDIT = ("Here is a Python module. Add a discount(self, pct: int) -> int method to EVERY "
        "Item class, returning the discounted total value. Output the COMPLETE modified "
        "file, nothing else.\\n\\n" + FENCE + "python\\n" + source() + "\\n" + FENCE)
def call(p, mt=512, thinking=True):
    payload = {"model":MODEL,"messages":[{"role":"user","content":p}],"max_tokens":mt,"temperature":0}
    # Reasoning tokens are invented, not copied from the prompt, so a drafter
    # cannot predict them. Left on, the code-edit run measures reasoning speed
    # and reports roughly the mixed-prompt number instead of the peak.
    if not thinking: payload["chat_template_kwargs"] = {"enable_thinking": False}
    body = json.dumps(payload).encode()
    # A User-Agent is required, not decoration: Cloudflare answers the default
    # Python-urllib one with 403 (its error 1010) before the request ever
    # reaches the Hub, so a tunnelled Hub fails while a LAN one works.
    headers = {"Content-Type":"application/json","Authorization":"Bearer "+KEY,
               "User-Agent":"spark-ai-hub-benchmark"}
    req = urllib.request.Request(HOST+"/v1/chat/completions", data=body, headers=headers)
    t = time.time()
    try:
        with urllib.request.urlopen(req, timeout=600) as r: d = json.loads(r.read())
    except urllib.error.HTTPError as e:
        raise SystemExit(f"HTTP {e.code} from {HOST}: {e.read().decode(errors='replace')[:300]}")
    except urllib.error.URLError as e:
        raise SystemExit(f"Cannot reach {HOST}: {e.reason}")
    return d["usage"]["completion_tokens"], time.time()-t
print("warmup (discarded)..."); call("hi", 32)
total_tok = total_t = 0
for label, p in PROMPTS:
    n, dt = call(p); total_tok += n; total_t += dt
    print(f"  {label:<10} {n} tok / {dt:.2f}s = {n/dt:.2f} tok/s")
print(f"Writing: {total_tok/total_t:.2f} tok/s ({total_tok} tok / {total_t:.2f}s across {len(PROMPTS)} mixed prompts)")
n, dt = call(EDIT, 3000, thinking=False)
print(f"  {'code-edit':<10} {n} tok / {dt:.2f}s = {n/dt:.2f} tok/s")
print(f"Editing: {n/dt:.2f} tok/s (code-edit, thinking off; the answer is already in the prompt,")
print(f"         so a drafter's proposals are nearly always accepted)")
EOF`}
              />
            )}
          </div>
        </Card>
      )}

      {/* ─── Cleanup ─── */}
      {!recipe.installed && !isBuilding && recipe.has_leftovers && (
        <Card className="gap-0 p-5 shadow-none">
          <SectionTitle>Cleanup</SectionTitle>
          <p className="m-0 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            Remove leftover images or volumes from a previous uninstall. This is destructive and should only be used when you want to free all remaining app data.
          </p>
          <div className="mt-3">
            <Button
              variant="outline"
              disabled={!!purging[recipe.slug]}
              onClick={() => { if (window.confirm(`Wipe all data for ${recipe.name}?`)) purgeRecipe(recipe.slug) }}
              className="border-warning/30 text-warning hover:bg-warning/10"
            >
              {purging[recipe.slug] ? 'Wiping cached data...' : 'Wipe cached data'}
            </Button>
          </div>
        </Card>
      )}
    </div>
  )
}

/* “Create copy” asks for one thing — the new slug — because everything else
 * about the copy is decided for you: it is a byte-for-byte copy in local/,
 * where no sync can reach it. A slug that is already taken comes back as a 409
 * with the daemon's own sentence, shown under the field the user just typed in.
 */
function ForkRecipeModal({ slug, onClose, onDone }) {
  const [value, setValue] = useState(() => suggestCopySlug(slug))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const inputRef = useRef(null)
  const valid = SLUG_PATTERN.test(value)

  useEffect(() => {
    const el = inputRef.current
    if (el) { el.focus(); el.select() }
  }, [])

  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const submit = async () => {
    if (!valid || busy) return
    setBusy(true)
    setError('')
    const res = await forkRecipe(slug, value)
    setBusy(false)
    if (!res.ok) {
      setError(res.error)
      return
    }
    onDone(res.data?.slug || value)
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="gap-3 border-border bg-card p-5 shadow-none sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="font-display text-base font-semibold tracking-tight">
            Create a copy
          </DialogTitle>
          <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
            The copy lives in <span className="font-mono text-foreground">~/.config/gospark/registry/local/</span> and is never
            overwritten by a sync. Everything else — compose file, ports, settings — comes with it.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-1.5">
          <Label
            htmlFor="fork-slug"
            className="text-[11px] font-semibold uppercase tracking-[0.1em] text-muted-foreground"
          >
            New slug
          </Label>
          <Input
            id="fork-slug"
            ref={inputRef}
            value={value}
            onChange={(e) => { setValue(e.target.value); setError('') }}
            onKeyDown={(e) => e.key === 'Enter' && submit()}
            spellCheck={false}
            className="h-9 font-mono text-xs"
          />
          <span className={`text-[11px] ${valid ? 'text-muted-foreground' : 'text-warning'}`}>
            Lowercase letters, digits and dashes, 2–63 characters.
          </span>
        </div>

        {error && <p className="m-0 text-xs leading-relaxed text-destructive">{error}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button onClick={submit} disabled={!valid || busy}>
            {busy ? 'Copying…' : 'Create copy'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ComposeEditor({ slug, local }) {
  const [content, setContent] = useState('')
  const [original, setOriginal] = useState('')
  const [defaultContent, setDefaultContent] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false

    const load = async () => {
      setLoading(true)
      setError('')
      setSaved(false)
      try {
        const res = await fetch(`/api/recipes/${slug}/compose`)
        if (!res.ok) throw new Error('load failed')
        const data = await res.json()
        if (cancelled) return
        setContent(data.content)
        setOriginal(data.content)
        setDefaultContent(data.default_content || data.content)
      } catch {
        if (!cancelled) setError('Failed to load docker-compose.yml')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    load()
    return () => { cancelled = true }
  }, [slug])

  const save = async () => {
    setSaving(true)
    setError('')
    try {
      const res = await fetch(`/api/recipes/${slug}/compose`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
      })
      if (!res.ok) throw new Error('save failed')
      setOriginal(content)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch {
      setError('Failed to save docker-compose.yml')
    } finally {
      setSaving(false)
    }
  }

  const resetToDefault = async () => {
    if (!window.confirm('Reset docker-compose.yml to the default recipe version?')) return
    setResetting(true)
    setError('')
    try {
      const res = await fetch(`/api/recipes/${slug}/compose/reset`, { method: 'POST' })
      if (!res.ok) throw new Error('reset failed')
      const data = await res.json()
      setContent(data.content)
      setOriginal(data.content)
      setDefaultContent(data.content)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch {
      setError('Failed to reset docker-compose.yml')
    } finally {
      setResetting(false)
    }
  }

  const dirty = content !== original
  const canReset = original !== defaultContent || content !== defaultContent

  return (
    <Card className="gap-0 p-5 shadow-none">
      <SectionTitle
        right={
          <div className="flex shrink-0 items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={!canReset || resetting || loading}
              onClick={resetToDefault}
            >
              {resetting ? 'Restoring...' : 'Restore Default'}
            </Button>
            <Button
              size="sm"
              disabled={!dirty || saving || loading}
              onClick={save}
            >
              {saving ? 'Saving...' : 'Save Changes'}
            </Button>
          </div>
        }
      >
        Compose Configuration
      </SectionTitle>

      <p className="m-0 max-w-2xl text-xs leading-relaxed text-muted-foreground">
        Edit the live `docker-compose.yml` for this recipe. Save keeps your custom version; restore brings back the default file from the registry.
      </p>
      {!local && (
        <p className="m-0 mt-2 max-w-2xl text-[11px] leading-relaxed text-warning">
          This recipe comes from a registry source: the next sync replaces these edits. Use “Create copy” to keep a version of your own.
        </p>
      )}

      <div className="mt-4">
        {loading ? (
          <div className="flex h-[420px] items-center justify-center rounded-lg border border-border bg-[#08080F] text-sm text-muted-foreground">
            Loading docker-compose.yml...
          </div>
        ) : (
          <YamlEditor
            value={content}
            onChange={setContent}
            className="h-[420px] w-full overflow-hidden rounded-lg border border-border bg-[#08080F] focus-within:border-primary/50"
          />
        )}
      </div>

      <div className="mt-2 flex min-h-[20px] flex-wrap items-center gap-3 text-xs">
        {saved && <span className="text-success">Saved. Relaunch or reinstall to apply.</span>}
        {dirty && !saved && <span className="text-warning">Unsaved changes</span>}
        {!dirty && canReset && !saved && <span className="text-muted-foreground">Using a customized compose file.</span>}
        {error && <span className="text-destructive">{error}</span>}
      </div>
    </Card>
  )
}

// What the Hub reports to coding agents on /v1/models, spelled out so the user
// knows what their client thinks this model can do.
const CAPABILITY_LABELS = {
  completion: 'Text generation',
  vision: 'Image input',
  video: 'Video input',
  tools: 'Tool calling',
  thinking: 'Reasoning',
}

// What an image/video recipe makes, from daemon/models/recipe.py MEDIA_CAPABILITIES.
const MEDIA_LABELS = {
  'image-generation': 'Image generation',
  'image-editing': 'Image editing',
  'multi-image-input': 'Multiple images + text',
  'text-to-video': 'Text-to-video',
  'image-to-video': 'Image-to-video',
  'first-last-frame': 'First/last frame',
  'multi-reference': 'Multiple references',
  'audio-reference': 'Audio reference',
  'video-editing': 'Video editing',
  'face-swap': 'Face swap',
  'video-with-audio': 'Video with sound',
  'orbit-360': '360° orbit',
  'image-to-3d': 'Image to 3D',
  'multi-view': 'Multiple views',
  'music-generation': 'Music generation',
}

function CapabilityField({ capabilities }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">Capabilities</span>
      <div className="flex flex-wrap gap-1.5">
        {capabilities.map((c) => (
          <Badge key={c} variant="secondary" className="text-[11px] font-normal">
            {CAPABILITY_LABELS[c] || c}
          </Badge>
        ))}
      </div>
      <p className="m-0 text-[10px] leading-4 text-muted-foreground">
        Reported to coding agents and other clients on <code className="font-mono">/v1/models</code>.
      </p>
    </div>
  )
}

function CodeBlock({ label, value }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    const text = String(value)
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1200) })
        .catch(() => fallbackCopy(text, setCopied))
    } else {
      fallbackCopy(text, setCopied)
    }
  }
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
      <div className="relative">
        <pre className="m-0 overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-border bg-muted p-3 pr-12 font-mono text-[11px] leading-relaxed text-foreground">
          {value}
        </pre>
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          onClick={copy}
          title="Copy"
          className="absolute top-2 right-2 bg-background"
        >
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
        </Button>
      </div>
    </div>
  )
}

function BenchmarkBlock({ value, secret }) {
  const [copied, setCopied] = useState(false)
  const [shown, setShown] = useState(false)
  // Same contract as Account's key card: masked on screen behind a Show, and
  // Copy hands over the real thing. The snippet needs a working key baked in
  // -- it is meant to run on another machine with nothing set up -- but this
  // page is open all day, so the key is not readable until asked for.
  const carries = hasSecret(value, secret)
  const display = maskIn(value, secret, shown)
  const copy = () => {
    const text = String(value)
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1200) })
        .catch(() => fallbackCopy(text, setCopied))
    } else {
      fallbackCopy(text, setCopied)
    }
  }
  return (
    <div className="flex flex-col gap-1.5 border-t border-border pt-4">
      <div className="flex items-center justify-between gap-3">
        <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">benchmark inference speed</span>
        {carries && (
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setShown(!shown)}
            title={shown ? 'Hide your API key' : 'Reveal your API key'}
            className="text-muted-foreground hover:text-foreground"
          >
            {shown ? 'Hide' : 'Show'} key
          </Button>
        )}
      </div>
      <div className="relative overflow-hidden rounded-lg border border-border">
        <div className="border-b border-border bg-muted px-3 py-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 font-mono text-[10px]">
            <span className="uppercase tracking-[0.12em] text-muted-foreground">prompts:</span>
            <span className="text-foreground"><span className="text-primary">code</span> · quicksort</span>
            <span className="text-border">|</span>
            <span className="text-foreground"><span className="text-primary">explainer</span> · TCP vs UDP</span>
            <span className="text-border">|</span>
            <span className="text-foreground"><span className="text-primary">prose</span> · Kyoto travel</span>
          </div>
          <div className="mt-1 font-mono text-[10px] text-muted-foreground">512 tok · temp=0 · warmup discarded</div>
        </div>
        <pre className="m-0 overflow-x-auto whitespace-pre-wrap break-all p-3 pr-12 font-mono text-[11px] leading-relaxed text-foreground">
          {display}
        </pre>
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          onClick={copy}
          title="Copy"
          className="absolute top-11 right-2 bg-background"
        >
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
        </Button>
      </div>
    </div>
  )
}

function fallbackCopy(text, setCopied) {
  const ta = document.createElement('textarea')
  ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'
  document.body.appendChild(ta); ta.select(); document.execCommand('copy'); document.body.removeChild(ta)
  setCopied(true); setTimeout(() => setCopied(false), 1200)
}

function TerminalPanel({ lines, isBuilding, isUpdating, isRunning, isReady, hasLogs, scrollRef, wide = false }) {
  const [autoScroll, setAutoScroll] = useState(true)

  useEffect(() => {
    if (autoScroll && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [lines.length, autoScroll, scrollRef])

  const handleScroll = () => {
    if (!scrollRef.current) return
    const { scrollTop, scrollHeight, clientHeight } = scrollRef.current
    setAutoScroll(scrollHeight - scrollTop - clientHeight < 40)
  }

  const showEmpty = !isBuilding && !isRunning && !hasLogs

  return (
    <div className={`log-terminal flex h-full flex-col overflow-hidden ${wide ? 'w-full' : ''}`}>
      <div className="flex shrink-0 items-center justify-between border-b border-border/60 bg-[#0C0C14] px-4 py-2.5">
        <div className="flex items-center gap-3">
          <div className="flex gap-1.5">
            <span className="h-2.5 w-2.5 rounded-full bg-[#ff5f57]/70" />
            <span className="h-2.5 w-2.5 rounded-full bg-[#febc2e]/70" />
            <span className="h-2.5 w-2.5 rounded-full bg-[#28c840]/70" />
          </div>
          <span className="font-mono text-[10px] text-muted-foreground">
            {isUpdating ? 'update' : isBuilding ? 'build' : 'container'} - logs
          </span>
        </div>
        <div className="flex items-center gap-2">
          {isBuilding && <span className="animate-pulse font-mono text-[10px] text-primary">{isUpdating ? '● updating' : '● building'}</span>}
          {!isBuilding && isRunning && isReady && <span className="font-mono text-[10px] text-success">● running</span>}
          {!isBuilding && isRunning && !isReady && <span className="animate-pulse font-mono text-[10px] text-warning">● starting</span>}
        </div>
      </div>

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto p-4 font-mono text-[11px] leading-5 selection:bg-primary/30"
      >
        {showEmpty && (
          <div className="italic text-muted-foreground/70">No logs. Launch the app to see output here.</div>
        )}
        {lines.map((l, i) => (
          <div
            key={i}
            className={
              l.includes('[error]') || l.includes('Error') || l.includes('FATAL') || l.includes('Traceback')
                ? 'whitespace-pre-wrap text-destructive [tab-size:2]'
                : l.includes('successfully') || l.includes('Started') || l.includes('Running on')
                ? 'whitespace-pre-wrap text-success [tab-size:2]'
                : 'whitespace-pre-wrap text-muted-foreground [tab-size:2]'
            }
          >
            {l}
          </div>
        ))}
        {(isBuilding || (isRunning && !isReady)) && lines.length > 0 && (
          <span className="animate-pulse text-primary">▋</span>
        )}
      </div>
    </div>
  )
}

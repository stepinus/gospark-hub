import { useEffect, useState } from 'react'
import { Moon } from 'lucide-react'
import { useStore } from '../store'
import { benchmarkRecipe, getBenchmarks } from '../lib/api'
import { useThemedLogo } from '../hooks/useThemedLogo'
import { useApiKey } from '../hooks/useApiKey'
import { hubOrigin } from '../lib/urls'
import CopyField from '../components/CopyField'
import { openUrl } from '../components/RecipeCard'
import { PageHeader, SectionTitle, EmptyState } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'

export default function Running() {
  const recipes = useStore((s) => s.recipes)
  const selectRecipe = useStore((s) => s.selectRecipe)
  const launchOrSwap = useStore((s) => s.launchOrSwap)
  const stopRecipe = useStore((s) => s.stopRecipe)
  const installing = useStore((s) => s.installing)
  const updating = useStore((s) => s.updating)

  const building = recipes.filter((r) => installing[r.slug] || updating[r.slug])

  // Last measurement per recipe, read once and refreshed after each run.
  const [benchmarks, setBenchmarks] = useState({})
  useEffect(() => {
    ;(async () => {
      const res = await getBenchmarks()
      if (res.ok) setBenchmarks(res.data?.benchmarks || {})
    })()
  }, [])

  const runBenchmark = async (slug) => {
    const res = await benchmarkRecipe(slug)
    if (res.ok) setBenchmarks((prev) => ({ ...prev, [slug]: res.data }))
    return res
  }
  const running = recipes.filter((r) => r.running || r.starting)
  const installed = recipes.filter((r) => r.installed && !r.running && !r.starting && !installing[r.slug] && !updating[r.slug])

  return (
    <div className="page flex flex-col gap-6">
      <PageHeader
        title="Running"
        actions={
          <>
            {running.length > 0 && (
              <Badge
                variant="outline"
                className="gap-1.5 border-success/30 bg-success/10 text-success"
              >
                <span className="h-1.5 w-1.5 rounded-full bg-success" />
                {running.length} app{running.length > 1 ? 's' : ''} active
              </Badge>
            )}
            {building.length > 0 && (
              <Badge variant="outline" className="gap-1.5 border-border bg-muted text-muted-foreground">
                <span className="font-mono">⟳</span>
                {building.length} installing
              </Badge>
            )}
          </>
        }
      />

      {running.length === 0 && building.length === 0 ? (
        <EmptyState
          icon={<Moon className="h-6 w-6" />}
          title="No apps running"
          description="Install and launch apps from the Store"
        />
      ) : (
        <div className="flex flex-col gap-3">
          {building.map((r) => (
            <BuildingCard key={r.slug} recipe={r} onSelect={selectRecipe} isUpdating={!!updating[r.slug]} />
          ))}
          {running.map((r) => (
            <RunningCard
              key={r.slug}
              recipe={r}
              onSelect={selectRecipe}
              onStop={stopRecipe}
              benchmark={benchmarks[r.slug]}
              onBenchmark={runBenchmark}
            />
          ))}
        </div>
      )}

      {/* Installed (Stopped) Section */}
      {installed.length > 0 && (
        <div>
          <SectionTitle hint={`${installed.length} stopped`}>Installed</SectionTitle>
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {installed.map((r) => (
              <StoppedCard key={r.slug} recipe={r} onSelect={selectRecipe} onLaunch={launchOrSwap} />
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function BuildingCard({ recipe, onSelect, isUpdating }) {
  const [logoFailed, setLogoFailed] = useState(false)
  const logoUrl = useThemedLogo(recipe.logo)

  return (
    <Card
      onClick={() => onSelect(recipe.slug)}
      className="cursor-pointer flex-row items-center gap-4 p-4 opacity-80 shadow-none transition-colors hover:ring-foreground/20"
    >
      <div className="shrink-0">
        {logoUrl && !logoFailed ? (
          <img
            src={logoUrl}
            alt={recipe.name}
            className="h-12 w-12 shrink-0 rounded-lg bg-muted object-contain p-2"
            onError={() => setLogoFailed(true)}
          />
        ) : (
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-lg bg-muted text-xl text-foreground">
            {recipe.icon || '◻'}
          </div>
        )}
      </div>

      <div className="min-w-0 flex-1">
        <h3 className="m-0 truncate font-display text-sm font-semibold text-foreground">{recipe.name}</h3>
        <p className="m-0 mt-0.5 truncate text-xs text-muted-foreground">{recipe.author}</p>
      </div>

      <Badge variant="outline" className="shrink-0 gap-1.5 border-border bg-muted text-muted-foreground">
        <span className="font-mono">⟳</span>
        {isUpdating ? 'Updating' : 'Installing'}
      </Badge>
    </Card>
  )
}

function RunningCard({ recipe, onSelect, onStop, benchmark, onBenchmark }) {
  const [logoFailed, setLogoFailed] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [measuring, setMeasuring] = useState(false)
  const [benchError, setBenchError] = useState('')
  const apiKey = useApiKey()

  const handleBenchmark = async (e) => {
    e.stopPropagation()
    setMeasuring(true)
    setBenchError('')
    const res = await onBenchmark(recipe.slug)
    if (!res.ok) setBenchError(res.error)
    setMeasuring(false)
  }
  const logoUrl = useThemedLogo(recipe.logo)
  const isReady = recipe.ready

  const handleStop = async (e) => {
    e.stopPropagation()
    setStopping(true)
    await onStop(recipe.slug)
    setStopping(false)
  }

  return (
    <Card
      onClick={() => onSelect(recipe.slug)}
      className="cursor-pointer gap-0 p-4 shadow-none transition-colors hover:ring-foreground/20 sm:p-5"
    >
      {/* On a phone the status and buttons drop to a row of their own, so the
          name keeps the width it needs. */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
        {/* Icon */}
        <div className="shrink-0">
          {logoUrl && !logoFailed ? (
            <img
              src={logoUrl}
              alt={recipe.name}
              className="h-12 w-12 shrink-0 rounded-lg bg-muted object-contain p-2"
              onError={() => setLogoFailed(true)}
            />
          ) : (
            <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-lg bg-muted text-xl text-foreground">
              {recipe.icon || '◻'}
            </div>
          )}
        </div>

        {/* Info */}
        <div className="min-w-0 flex-1">
          <h3 className="m-0 truncate font-display text-sm font-semibold text-foreground">{recipe.name}</h3>
          <p className="m-0 mt-0.5 truncate text-xs text-muted-foreground">{recipe.author}</p>
        </div>

        {/* Status */}
        <div className="flex w-full shrink-0 items-center justify-end gap-3 sm:w-auto sm:gap-4">
          <Badge
            variant="outline"
            className={
              isReady
                ? 'gap-1.5 border-success/30 bg-success/10 text-success'
                : 'gap-1.5 border-warning/30 bg-warning/10 text-warning'
            }
          >
            <span className={`h-1.5 w-1.5 rounded-full ${isReady ? 'bg-success' : 'bg-warning'}`} />
            {isReady ? 'Running' : 'Starting'}
          </Badge>

          {/* Port — only for recipes that still publish one; a proxied app
              has no host port to show, it lives under /app/{slug}/ */}
          {recipe.ui?.port && !recipe.app_url && (
            <span className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[11px] tabular-nums text-muted-foreground">
              :{recipe.ui.port}
            </span>
          )}

          {/* GPU / RAM lanes */}
          <div className="hidden items-center gap-3 lg:flex">
            <MemoryLane label="GPU" value={`${recipe.gpu_pct || 0}%`} pct={recipe.gpu_pct || 0} />
            <MemoryLane label="RAM" value={recipe.ram_gb ? `${recipe.ram_gb}G` : '—'} />
          </div>

          {/* Actions */}
          {isReady && (
            <Button asChild variant="outline" size="sm">
              <a
                href={openUrl(recipe)}
                target="_blank"
                rel="noreferrer"
                onClick={(e) => e.stopPropagation()}
              >
                Open
              </a>
            </Button>
          )}
          {isReady && (
            <Button variant="outline" size="sm" disabled={measuring} onClick={handleBenchmark}>
              {measuring ? '⟳ Measuring…' : 'Benchmark'}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            disabled={stopping}
            onClick={handleStop}
            className="text-muted-foreground hover:text-foreground"
          >
            {stopping ? '...' : 'Stop'}
          </Button>
        </div>
      </div>

      {/* Everything a client needs for this model, one click each: the Hub's
          /v1 forwards to whatever model a request names, so the address is the
          same wherever the model runs. Only recipes that declare an
          integration block serve an API. */}
      {isReady && recipe.integration && (
        <div className="mt-3 grid gap-3 border-t border-border pt-3 sm:grid-cols-3">
          <CopyField label="API endpoint" value={`${hubOrigin()}/v1`} />
          <CopyField label="Model" value={recipe.integration.model_id || recipe.name} />
          <CopyField label="Token" value={apiKey || 'not-needed'} secret={Boolean(apiKey)} />
        </div>
      )}

      {(benchmark || benchError) && (
        <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 border-t border-border pt-2 text-[11px] text-muted-foreground">
          {benchError && <span className="text-error">{benchError}</span>}
          {benchmark && (
            <>
              <span>
                prefill <b className="font-mono tabular-nums text-foreground">{benchmark.prefill_tps}</b> tok/s
                <span>
                  {' '}· {benchmark.prompt_tokens} tok / {benchmark.ttft_ms} ms
                  {benchmark.prefill_naive_tps ? ` · naive ${benchmark.prefill_naive_tps}` : ''}
                  {benchmark.overhead_ms ? ` · +${Math.round(benchmark.overhead_ms)} ms fixed` : ''}
                </span>
              </span>
              <span>
                decode <b className="font-mono tabular-nums text-foreground">{benchmark.decode_tps}</b> tok/s
                <span> · {benchmark.decode_tokens} tok / {benchmark.total_ms} ms</span>
              </span>
              <span>
                memory{' '}
                <b className="font-mono tabular-nums text-foreground">
                  {benchmark.memory_bytes ? `${(benchmark.memory_bytes / 1024 ** 3).toFixed(1)} GiB` : '—'}
                </b>
                {benchmark.memory_source ? <span> · {benchmark.memory_source}</span> : null}
              </span>
            </>
          )}
        </div>
      )}
    </Card>
  )
}

function StoppedCard({ recipe, onSelect, onLaunch }) {
  const [logoFailed, setLogoFailed] = useState(false)
  const [launching, setLaunching] = useState(false)
  const logoUrl = useThemedLogo(recipe.logo)

  const handleLaunch = async (e) => {
    e.stopPropagation()
    if (recipe.requires_hf_token) {
      const res = await fetch('/api/system/hf-token')
      const { has_token } = await res.json()
      if (!has_token) {
        onSelect(recipe.slug)
        return
      }
    }
    setLaunching(true)
    await onLaunch(recipe.slug)
    setLaunching(false)
  }

  return (
    <Card
      onClick={() => onSelect(recipe.slug)}
      className="cursor-pointer flex-row items-center gap-3 p-4 shadow-none transition-colors hover:ring-foreground/20"
    >
      <div className="shrink-0">
        {logoUrl && !logoFailed ? (
          <img
            src={logoUrl}
            alt={recipe.name}
            className="h-10 w-10 shrink-0 rounded-lg bg-muted object-contain p-1.5"
            onError={() => setLogoFailed(true)}
          />
        ) : (
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-muted text-lg text-foreground">
            {recipe.icon || '◻'}
          </div>
        )}
      </div>

      <div className="min-w-0 flex-1">
        <h3 className="m-0 truncate font-display text-sm font-semibold text-foreground">{recipe.name}</h3>
        <span className="text-[11px] text-muted-foreground">Stopped</span>
      </div>

      <Button size="sm" disabled={launching} onClick={handleLaunch} className="shrink-0">
        {launching ? '...' : 'Launch'}
      </Button>
    </Card>
  )
}

/* A labelled memory lane: the number is what the recipe reports, the bar is the
   share it reports. RAM arrives as gigabytes with no total to divide by, so it
   stays a number and only GPU (a percentage) fills a track. */
function MemoryLane({ label, value, pct }) {
  return (
    <div className="flex w-[72px] flex-col gap-1">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
        <span className="font-mono text-[10px] tabular-nums text-foreground">{value}</span>
      </div>
      {typeof pct === 'number' && <Progress value={pct} className="h-1" />}
    </div>
  )
}

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { cn } from 'cn'
import { useStore } from '../store'
import { useThemedLogo } from '../hooks/useThemedLogo'
import { buildLabel, displayName, isAgentModel, isModel } from '../models'
import { openUrl } from './RecipeCard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'

// What is running on this Spark, in the header of every page.
//
// A count on a sidebar icon said how many things were up, never which; the
// Running page said which, but only once you went there. Here each one is a
// chip that names itself the way you would: its kind (an app you open, a
// language model agents talk to at /v1, a media model they call through
// /mcp), the model, and for models the build. The memory gauge sits at the
// end because on a Spark that is what decides whether the next launch fits.

const MAX_PILLS = 4

const KIND = {
  app: { label: 'App', className: 'border-transparent bg-muted text-muted-foreground' },
  llm: { label: 'LLM', className: 'border-primary/30 bg-primary/15 text-primary' },
  media: { label: 'Media', className: 'border-tertiary/30 bg-tertiary/15 text-tertiary' },
}

function kindOf(recipe) {
  if (isAgentModel(recipe)) return 'llm'
  if (isModel(recipe)) return 'media'
  return 'app'
}

// The /v1 default is the largest LLM up -- the same order the daemon's
// router uses (params, then memory held).
function bySize(a, b) {
  return (b.params_b || 0) - (a.params_b || 0) || (b.memory_gb || 0) - (a.memory_gb || 0)
}

const KIND_ORDER = { llm: 0, media: 1, app: 2 }

// `scroll`: phones, where the tray has a row of its own that scrolls
// sideways — every chip keeps its full width and none is folded into "+N".
export default function RunningTray({ scroll = false }) {
  const recipes = useStore((s) => s.recipes)
  const installing = useStore((s) => s.installing)
  const updating = useStore((s) => s.updating)
  const metrics = useStore((s) => s.metrics)

  const building = recipes.filter((r) => installing[r.slug] || updating[r.slug])
  const up = recipes
    .filter((r) => (r.running || r.starting) && !installing[r.slug] && !updating[r.slug])
    .sort((a, b) => KIND_ORDER[kindOf(a)] - KIND_ORDER[kindOf(b)] || bySize(a, b))
  const defaultLlm = up.find((r) => kindOf(r) === 'llm' && r.running && r.ready)

  const items = [...building, ...up]
  const shown = scroll ? items : items.slice(0, MAX_PILLS)
  const hidden = items.length - shown.length

  return (
    <div className={cn('flex items-center gap-1.5', scroll ? 'w-max' : 'min-w-0 flex-1')}>
      {items.length === 0 ? (
        <Link
          to="/running"
          className="flex items-center gap-2 rounded-lg px-2 py-1 text-[11px] text-muted-foreground no-underline transition-colors hover:text-foreground"
        >
          <span className="h-1.5 w-1.5 rounded-full bg-muted-foreground/60" />
          Nothing running
        </Link>
      ) : (
        <>
          {shown.map((r) => (
            <Pill
              key={r.slug}
              recipe={r}
              building={!!(installing[r.slug] || updating[r.slug])}
              isDefault={r === defaultLlm}
              scroll={scroll}
            />
          ))}
          {hidden > 0 && (
            <Link
              to="/running"
              title="Everything running on this Spark"
              className="shrink-0 rounded-lg border border-border px-2 py-1 font-mono text-[11px] font-semibold tabular-nums text-muted-foreground no-underline transition-colors hover:border-muted-foreground hover:text-foreground"
            >
              +{hidden}
            </Link>
          )}
        </>
      )}
      {metrics?.ram_total_gb > 0 && <MemoryGauge used={metrics.ram_used_gb} total={metrics.ram_total_gb} />}
    </div>
  )
}

function Pill({ recipe, building, isDefault, scroll }) {
  const selectRecipe = useStore((s) => s.selectRecipe)
  const stopRecipe = useStore((s) => s.stopRecipe)
  const logoUrl = useThemedLogo(recipe.logo)
  const [logoFailed, setLogoFailed] = useState(false)
  const kind = kindOf(recipe)
  const ready = recipe.running && recipe.ready

  const state = building
    ? { dot: 'bg-muted-foreground', label: 'Building' }
    : ready
      ? { dot: 'bg-success', label: 'Running' }
      : { dot: 'bg-warning', label: 'Starting' }

  // Models are told apart by build; apps by who made them.
  const detail = kind === 'app'
    ? recipe.author
    : [buildLabel(recipe), recipe.engine].filter(Boolean).join(' · ')
  const memory = recipe.memory_gb ? `${Math.round(recipe.memory_gb)} GB` : null
  const name = kind === 'app' ? recipe.name : displayName(recipe)

  return (
    <Card
      className={cn(
        'group flex-row items-center gap-2 rounded-lg border bg-card p-0 py-1 pl-1.5 pr-2 shadow-none ring-0 transition-colors',
        scroll ? 'max-w-[250px] shrink-0' : 'min-w-0 max-w-[290px] shrink',
        isDefault ? 'border-primary/40' : 'border-border',
      )}
      title={`${recipe.name} — ${state.label}${memory ? ` · holds ${memory}` : ''}${isDefault ? ' · /v1 default' : ''}`}
    >
      <button
        type="button"
        onClick={() => selectRecipe(recipe.slug)}
        className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 border-none bg-transparent p-0 text-left"
      >
        <span className="relative shrink-0">
          {logoUrl && !logoFailed ? (
            <img
              src={logoUrl}
              alt=""
              onError={() => setLogoFailed(true)}
              className="h-7 w-7 rounded-md bg-muted object-contain p-1"
            />
          ) : (
            <span className="flex h-7 w-7 items-center justify-center rounded-md bg-muted text-xs text-foreground">
              {recipe.icon || '◻'}
            </span>
          )}
          <span className={cn('absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-card', state.dot)} />
        </span>
        <span className="flex min-w-0 flex-col leading-tight">
          <span className="truncate text-[12px] font-semibold text-foreground">{name}</span>
          <span className="flex min-w-0 items-center gap-1 text-[10px] text-muted-foreground">
            <Badge
              variant="outline"
              className={cn('h-4 shrink-0 rounded px-1 text-[9px] font-semibold uppercase tracking-wide', KIND[kind].className)}
            >
              {KIND[kind].label}
            </Badge>
            {isDefault && <span className="shrink-0 font-semibold text-primary">default</span>}
            <span className="truncate">{building ? 'building…' : !ready ? 'starting…' : detail}</span>
          </span>
        </span>
      </button>

      {/* Actions appear on hover so the resting tray reads as a list. */}
      <span className="hidden shrink-0 items-center gap-1 group-hover:flex">
        {kind === 'app' && ready && (
          <Button asChild size="xs" className="h-5 px-1.5 text-[10px] font-semibold">
            <a href={openUrl(recipe)} target="_blank" rel="noreferrer" title={`Open ${recipe.name}`}>
              Open ↗
            </a>
          </Button>
        )}
        {!building && (
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => stopRecipe(recipe.slug)}
            title={`Stop ${recipe.name}`}
            className="h-5 w-5 text-muted-foreground hover:text-error"
          >
            ×
          </Button>
        )}
      </span>
    </Card>
  )
}

// What is left, not what is taken: the number a model's memory is held
// against when deciding what else can launch.
function MemoryGauge({ used, total }) {
  const free = Math.max(0, total - used)
  // The bar is the free share too, so the number and the bar say one thing.
  const pct = Math.min(100, Math.round((free / total) * 100))
  const tone = pct <= 10
    ? '[&>[data-slot=progress-indicator]]:bg-error'
    : pct <= 25 ? '[&>[data-slot=progress-indicator]]:bg-warning' : ''
  return (
    <Link
      to="/system"
      title={`${free.toFixed(1)} GB free of ${total.toFixed(0)} GB — open System Monitor`}
      className="ml-1 flex shrink-0 flex-col gap-1 rounded-lg px-2 py-1 no-underline transition-colors hover:bg-muted"
    >
      <span className="whitespace-nowrap text-[11px] tabular-nums text-muted-foreground">
        <span className="font-mono text-[13px] font-semibold text-foreground">{Math.round(free)} GB</span> free
      </span>
      <Progress value={pct} className={cn('h-1 w-16', tone)} />
    </Link>
  )
}

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useStore } from '../store'
import { useThemedLogo } from '../hooks/useThemedLogo'
import HideButton from './HideButton'
import { posterFor } from '../covers'
import { formatParams } from './RecipeCard'
import { speedLabel } from '../models'
import { openUrl as openUrlFor } from './RecipeCard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

function formatMonth(date) {
  if (!date) return null
  const [year, month] = date.split('-')
  return month ? `${MONTHS[Number(month) - 1]} ${year}` : year
}

// The fact the catalog is currently sorted by. It is promoted out of the spec
// chips into its own chip, so the value you are ranking on is readable without
// having to read the tile.
const HIGHLIGHTS = {
  release: { label: 'Released', value: (r) => formatMonth(r.release_date) },
  params: { label: 'Params', value: (r) => (r.params_b != null ? formatParams(r) : null) },
  size: { label: 'On disk', value: (r) => (r.weights_gb != null ? `${r.weights_gb} GB` : null) },
  speed: { label: 'Speed', value: (r) => speedLabel(r) },
}

// Tokens/sec keeps its own slot whatever the catalog is sorted by — it is the
// one number that decides whether a build is usable day to day.

// A poster tile for the catalog rows: the artwork on top, then the title and
// the facts that tell one build from another underneath it. The name is
// allowed to wrap to two lines and is never truncated mid-word — long builds
// like "Nemotron-3.5 Lightning 30B-A3B NVFP4 + DSpark" have to stay readable.
export default function PosterCard({ recipe, highlight = null }) {
  const installing = useStore((s) => s.installing)
  const updating = useStore((s) => s.updating)
  const installRecipe = useStore((s) => s.installRecipe)
  const requestLaunch = useStore((s) => s.requestLaunch)
  const [artFailed, setArtFailed] = useState(false)
  const [logoFailed, setLogoFailed] = useState(false)

  const logoUrl = useThemedLogo(recipe.logo)
  const isBusy = !!installing[recipe.slug] || !!updating[recipe.slug]
  const openUrl = openUrlFor(recipe)

  const status = isBusy
    ? { label: 'Building', dot: 'bg-muted-foreground animate-pulse' }
    : recipe.running && recipe.ready
      ? { label: 'Running', dot: 'bg-primary' }
      : recipe.running || recipe.starting
        ? { label: 'Starting', dot: 'bg-warning animate-pulse' }
        : recipe.installed
          ? { label: 'Installed', dot: 'bg-muted-foreground/50' }
          : null

  // The facts that actually differentiate one build of a model from another.
  // They are chips rather than a dim run-on line: at 196px a dot-joined line
  // truncates, and truncating is what buried them.
  const specs = [
    recipe.params_b != null && { id: 'params', text: formatParams(recipe) },
    recipe.quantization && { id: 'quant', text: recipe.quantization },
    recipe.weights_gb != null && { id: 'size', text: `${recipe.weights_gb} GB` },
    recipe.tokens_per_second != null && { id: 'speed', text: speedLabel(recipe) },
    highlight === 'release' && { id: 'release', text: formatMonth(recipe.release_date) },
  ].filter((s) => s && s.text)

  const badge = HIGHLIGHTS[highlight]
  const badgeValue = badge?.value(recipe)
  const speed = recipe.tokens_per_second
  const speedLeads = highlight === 'speed'
  // Whatever the promoted chip already states is dropped from the chips —
  // printing tok/s twice on one tile is noise, not emphasis.
  const chips = specs.filter((s) => s.id !== highlight && !(s.id === 'speed' && speed != null))

  return (
    <Link
      to={`/app/${recipe.slug}`}
      className="group relative block w-[calc(50%-6px)] shrink-0 no-underline text-inherit sm:w-[196px]"
      title={recipe.name}
    >
      <Card className="gap-0 border-border bg-card p-0 shadow-none transition-colors group-hover:ring-primary/40 [--card-spacing:0px]">
        <div className="relative aspect-[2/3] w-full overflow-hidden bg-muted">
          {!artFailed && (
            <img
              src={posterFor(recipe)}
              alt=""
              loading="lazy"
              onError={() => setArtFailed(true)}
              className="absolute inset-0 h-full w-full object-cover"
            />
          )}

          {/* Vendor logo, always legible against the art. */}
          {logoUrl && !logoFailed && (
            <span className="absolute left-2 top-2 flex h-8 w-8 items-center justify-center rounded-md border border-border bg-background/70 p-1">
              <img
                src={logoUrl}
                alt=""
                loading="lazy"
                onError={() => setLogoFailed(true)}
                className="h-full w-full object-contain"
              />
            </span>
          )}

          <HideButton slug={recipe.slug} always className="absolute right-2 top-2 z-20 border-border bg-background/70 text-muted-foreground hover:text-foreground" />
        </div>

        {/* Title and metadata, under the artwork rather than over it. */}
        <div className="flex flex-col gap-1.5 px-2.5 pb-2.5 pt-2">
          {status && (
            <p className="m-0 flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
              <span className={`h-1.5 w-1.5 rounded-full ${status.dot}`} />
              {status.label}
            </p>
          )}
          <h3 className="m-0 font-display text-[13px] font-semibold leading-snug text-foreground line-clamp-2 break-words">
            {recipe.name}
          </h3>

          {(badgeValue || speed != null || chips.length > 0) && (
            <div className="flex flex-wrap items-center gap-1">
              {badgeValue && !speedLeads && (
                <Badge variant="outline" className="h-4 gap-1 border-border px-1.5 text-[10px] font-mono tabular-nums text-foreground">
                  <span className="text-[9px] uppercase tracking-[0.08em] text-muted-foreground">{badge.label}</span>
                  {badgeValue}
                </Badge>
              )}
              {speed != null && (
                <Badge
                  variant="outline"
                  className={`h-4 px-1.5 font-mono text-[10px] tabular-nums ${
                    speedLeads ? 'border-primary/40 text-primary' : 'border-border text-primary'
                  }`}
                >
                  {speedLeads && (
                    <span className="text-[9px] uppercase tracking-[0.08em] text-primary/70">Speed</span>
                  )}
                  {speed} tok/s
                </Badge>
              )}
              {chips.map((s) => (
                <Badge
                  key={s.id}
                  variant="outline"
                  className="h-4 border-border/60 bg-muted px-1.5 font-mono text-[10px] tabular-nums text-muted-foreground"
                >
                  {s.text}
                </Badge>
              ))}
            </div>
          )}

          <p className="m-0 truncate text-[11px] text-muted-foreground/70">{recipe.author}</p>

          <div className="mt-0.5">
            {recipe.running && recipe.ready ? (
              <Button asChild size="xs" className="w-full">
                <a href={openUrl} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
                  {'Open ↗'}
                </a>
              </Button>
            ) : !recipe.installed && !recipe.starting && !isBusy ? (
              <Button
                variant="outline"
                size="xs"
                className="w-full"
                onClick={(e) => { e.preventDefault(); e.stopPropagation(); installRecipe(recipe.slug) }}
              >
                Install
              </Button>
            ) : recipe.installed && !recipe.running && !recipe.starting && !isBusy ? (
              <Button
                variant="outline"
                size="xs"
                className="w-full hover:border-primary/40 hover:text-primary"
                onClick={(e) => { e.preventDefault(); e.stopPropagation(); requestLaunch(recipe.slug) }}
              >
                Launch
              </Button>
            ) : (
              <span className="block w-full rounded-lg border border-border py-1 text-center text-[11px] font-medium text-muted-foreground">
                Details
              </span>
            )}
          </div>
        </div>
      </Card>
    </Link>
  )
}

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useStore } from '../store'
import { useThemedLogo } from '../hooks/useThemedLogo'
import HideButton from './HideButton'
import { speedLabel } from '../models'
import { openUrl } from '../lib/urls'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

// Where "Open" points. Lives in lib/urls.js with everything else the Hub says
// about its own address; re-exported here because ModelList, PosterCard,
// Hero, Running and RecipeDetail all import it from this module.
export { openUrl }

// "35B-A3B" for MoE (total + active), plain "27B" for dense.
export function formatParams(recipe) {
  const b = (n) => (Number.isInteger(n) ? n : n.toFixed(1))
  const total = `${b(recipe.params_b)}B`
  return recipe.active_params_b != null ? `${total}-A${b(recipe.active_params_b)}B` : total
}

// The chip holding the value the list is sorted by is filled in, so the fact
// you are ranking on stands out from the specs around it.
function chipClass(active) {
  return active
    ? 'border-primary bg-primary text-primary-foreground'
    : 'border-border bg-muted text-muted-foreground'
}

// `hideCategories` is set for the Ready-to-Serve Models section, where every
// card is an LLM and the category chip carries no information.
export default function RecipeCard({ recipe, hideCategories = false, highlight = null }) {
  const installing = useStore((s) => s.installing)
  const updating = useStore((s) => s.updating)
  const installRecipe = useStore((s) => s.installRecipe)
  const [logoFailed, setLogoFailed] = useState(false)

  const logoUrl = useThemedLogo(recipe.logo)
  const isBuilding = !!installing[recipe.slug]
  const isUpdating = !!updating[recipe.slug]
  const isBusy = isBuilding || isUpdating

  const handleInstall = (e) => {
    e.preventDefault()
    e.stopPropagation()
    installRecipe(recipe.slug)
  }

  const recipeCategories = Array.isArray(recipe.categories) && recipe.categories.length > 0
    ? recipe.categories
    : [recipe.category]

  return (
    <Link to={`/app/${recipe.slug}`} className="group relative block no-underline text-inherit">
      <Card className="relative gap-0 overflow-hidden rounded-lg border-border bg-card p-4 shadow-none transition-colors group-hover:border-primary/30">
        {/* Running indicator - top border */}
        {recipe.running && (
          <div
            className={`absolute inset-x-0 top-0 h-[2px] ${recipe.ready ? 'bg-primary' : 'bg-warning animate-pulse'}`}
          />
        )}

        {/* The hide control gets its own strip at the top of the card: it never
            lies on the action button, whatever the card contains. */}
        <div className="mb-1 flex items-center justify-end">
          <HideButton slug={recipe.slug} always />
        </div>

        <div className="flex items-start gap-3.5">
          {/* Icon */}
          {logoUrl && !logoFailed ? (
            <img
              src={logoUrl}
              alt={recipe.name}
              className="h-14 w-14 shrink-0 rounded-md border border-border bg-muted object-contain p-2"
              onError={() => setLogoFailed(true)}
            />
          ) : (
            <div className="flex h-14 w-14 shrink-0 items-center justify-center rounded-md border border-border bg-muted text-2xl">
              {recipe.icon || '◻'}
            </div>
          )}

          {/* Content */}
          <div className="min-w-0 flex-1">
            {/* Wraps to two lines rather than truncating — names like
                "Nemotron-3.5 Lightning 30B-A3B NVFP4 + DSpark" lose their
                meaning the moment the tail is cut off. */}
            <h3 className="m-0 line-clamp-2 break-words font-display text-sm font-semibold leading-tight text-foreground" title={recipe.name}>
              {recipe.name}
            </h3>
            <p className="m-0 mt-0.5 text-[11px] text-muted-foreground/80">{recipe.author}</p>
            <p className="m-0 mt-1.5 line-clamp-1 text-xs text-muted-foreground">{recipe.description}</p>

            {/* Tags */}
            <div className="mt-2 flex flex-wrap items-center gap-1.5">
              {!hideCategories && recipeCategories.slice(0, 2).map((cat) => (
                <Badge key={cat} variant="outline" className="h-4 px-1.5 text-[10px] text-muted-foreground">
                  {cat}
                </Badge>
              ))}
              {!recipe.docker?.gpu && (
                <Badge variant="outline" className="h-4 px-1.5 text-[10px] text-muted-foreground">CPU</Badge>
              )}
              {recipe.engine && (
                <Badge variant="outline" className="h-4 px-1.5 text-[10px] text-muted-foreground">{recipe.engine}</Badge>
              )}
              {recipe.arch && (
                <Badge variant="outline" className="h-4 px-1.5 text-[10px] uppercase text-muted-foreground">
                  {recipe.arch === 'moe' ? 'MoE' : 'Dense'}
                </Badge>
              )}
              {recipe.params_b != null && (
                <Badge variant="outline" className={`h-4 px-1.5 font-mono text-[10px] tabular-nums ${chipClass(highlight === 'params')}`}>
                  {formatParams(recipe)}
                </Badge>
              )}
              {recipe.quantization && (
                <Badge variant="outline" className={`h-4 px-1.5 font-mono text-[10px] tabular-nums ${chipClass(false)}`}>
                  {recipe.quantization}
                </Badge>
              )}
              {recipe.weights_gb != null && (
                <Badge variant="outline" className={`h-4 px-1.5 font-mono text-[10px] tabular-nums ${chipClass(highlight === 'size')}`}>
                  {recipe.weights_gb} GB
                </Badge>
              )}
              {recipe.tokens_per_second != null && (
                <Badge
                  variant="outline"
                  className={`h-4 px-1.5 font-mono text-[10px] tabular-nums ${highlight === 'speed'
                    ? chipClass(true)
                    : 'border-primary/30 bg-primary/10 text-primary'}`}
                >
                  {speedLabel(recipe)}
                </Badge>
              )}
            </div>
          </div>

          {/* Action */}
          <div className="mt-1 flex shrink-0 flex-col items-end gap-1.5">
            {isBuilding && (
              <span className="animate-pulse text-xs font-medium text-primary">
                <span className="inline-block animate-spin">⟳</span> Building
              </span>
            )}
            {isUpdating && (
              <span className="animate-pulse text-xs font-medium text-primary">
                <span className="inline-block animate-spin">⟳</span> Updating
              </span>
            )}
            {!isBusy && recipe.running && recipe.ready && (
              <Button asChild variant="secondary" size="xs">
                <a href={openUrl(recipe)} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
                  Open
                </a>
              </Button>
            )}
            {!isBusy && recipe.starting && (
              <span className="animate-pulse text-[11px] font-medium text-warning">Starting...</span>
            )}
            {!isBusy && !recipe.running && !recipe.starting && !recipe.installed && (
              <Button size="xs" onClick={handleInstall}>
                Install
              </Button>
            )}
            {!isBusy && !recipe.running && !recipe.starting && recipe.installed && (
              <Badge variant="outline" className="h-5 px-2 text-[11px] text-muted-foreground">Stopped</Badge>
            )}
          </div>
        </div>
      </Card>
    </Link>
  )
}

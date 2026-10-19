import { useEffect, useState } from 'react'
import { useStore } from '../store'
import { useThemedLogo } from '../hooks/useThemedLogo'
import { backdropFor } from '../covers'
import { formatParams } from './RecipeCard'
import { speedLabel } from '../models'
import { openUrl as openUrlFor } from './RecipeCard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

const ROTATE_MS = 9000

// Featured banner that cycles through a handful of titles. Rotation pauses
// while the pointer is over the hero. The backdrop is the artwork; the copy
// sits on top of it over the artwork's own blend, and the badges and buttons
// are the same primitives the rest of the page uses.
export default function Hero({ picks }) {
  const [index, setIndex] = useState(0)
  const [paused, setPaused] = useState(false)
  const selectRecipe = useStore((s) => s.selectRecipe)
  const installRecipe = useStore((s) => s.installRecipe)

  // Keep the index valid when the pick list changes length. The wrap happens
  // in a frame callback rather than in the effect body, and every read below
  // clamps too, so a shrinking pick list never points at nothing.
  useEffect(() => {
    const frame = requestAnimationFrame(() => setIndex((i) => (picks.length ? i % picks.length : 0)))
    return () => cancelAnimationFrame(frame)
  }, [picks.length])

  useEffect(() => {
    if (paused || picks.length < 2) return
    const t = setInterval(() => setIndex((i) => (i + 1) % picks.length), ROTATE_MS)
    return () => clearInterval(t)
  }, [paused, picks.length])

  const active = picks.length ? index % picks.length : 0
  const recipe = picks[active]
  const logoUrl = useThemedLogo(recipe?.logo)
  if (!recipe) return null

  const meta = [
    recipe.author,
    recipe.params_b != null && formatParams(recipe),
    recipe.quantization,
    speedLabel(recipe),
  ].filter(Boolean)

  const openUrl = openUrlFor(recipe)
  const live = recipe.running || recipe.starting

  return (
    // Tall enough that a 2.56:1 backdrop is only lightly cropped — at 46vh
    // the band was ~3.4:1 and sliced the subject's head off.
    <Card
      className="relative mx-6 h-[clamp(340px,46vh,480px)] gap-0 overflow-hidden border-border bg-card p-0 shadow-none [--card-spacing:0px]"
      style={{ background: 'var(--hero-gradient)' }}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
    >
      {/* Backdrops are stacked and cross-faded so the swap never flashes. */}
      {picks.map((r, i) => (
        <img
          key={r.slug}
          src={backdropFor(r)}
          alt=""
          aria-hidden={i !== active}
          className={`absolute inset-0 h-full w-full object-cover transition-opacity duration-1000 ${
            i === active ? 'opacity-100' : 'opacity-0'
          }`}
        />
      ))}

      {/* Blend the art into the app background on the left, so the copy keeps
          its contrast whatever the picture is. */}
      <div aria-hidden className="absolute inset-0" style={{ background: 'var(--hero-overlay-left)' }} />

      <div className="relative flex h-full max-w-3xl flex-col justify-center gap-3 px-6 sm:px-8">
        <div className="flex items-center gap-3">
          {logoUrl && (
            <img
              src={logoUrl}
              alt=""
              className="h-14 w-14 rounded-lg border border-border bg-background/40 object-contain p-2"
            />
          )}
          <Badge
            variant={live ? 'default' : 'outline'}
            className={live ? '' : 'border-border bg-background/60 text-muted-foreground'}
          >
            {live ? 'Now Running' : 'Featured'}
          </Badge>
        </div>

        {/* Wraps freely — hero titles are never truncated. */}
        <h1 className="m-0 font-display text-[clamp(1.6rem,3.4vw,2.6rem)] font-semibold leading-tight tracking-tight text-foreground">
          {recipe.name}
        </h1>

        <p className="m-0 font-mono text-xs tabular-nums text-muted-foreground">{meta.join('  ·  ')}</p>

        <p className="m-0 max-w-xl text-sm leading-relaxed text-muted-foreground line-clamp-2">
          {recipe.description}
        </p>

        <div className="mt-2 flex flex-wrap items-center gap-2">
          {recipe.running && recipe.ready ? (
            <Button asChild size="lg">
              <a href={openUrl} target="_blank" rel="noreferrer">{'▶  Open'}</a>
            </Button>
          ) : !recipe.installed && !recipe.starting ? (
            <Button size="lg" onClick={() => installRecipe(recipe.slug)}>
              Install
            </Button>
          ) : (
            <Button size="lg" onClick={() => selectRecipe(recipe.slug)}>
              View Details
            </Button>
          )}
          <Button variant="outline" size="lg" onClick={() => selectRecipe(recipe.slug)}>
            More Info
          </Button>
        </div>
      </div>

      {picks.length > 1 && (
        <div className="absolute bottom-5 right-8 flex items-center gap-1.5">
          {picks.map((r, i) => (
            <button
              key={r.slug}
              onClick={() => setIndex(i)}
              aria-label={`Show ${r.name}`}
              className={`h-1.5 cursor-pointer rounded-full border-none transition-colors ${
                i === active ? 'w-6 bg-primary' : 'w-1.5 bg-muted-foreground/40 hover:bg-muted-foreground'
              }`}
            />
          ))}
        </div>
      )}
    </Card>
  )
}

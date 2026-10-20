import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Search } from 'lucide-react'
import { useStore } from '../store'
import RecipeCard from '../components/RecipeCard'
import PosterCard from '../components/PosterCard'
import CardRow from '../components/CardRow'
import { byLab, byRelease } from '../components/ModelShelves'
import { isModel } from '../models'
import { EmptyState } from '@/components/page-shell'
import { Button } from '@/components/ui/button'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

const CATEGORIES = [
  { id: 'all', label: 'All' },
  { id: 'llm', label: 'LLM Apps' },
  { id: 'image-gen', label: 'Image Gen' },
  { id: 'video-gen', label: 'Video Gen' },
  { id: '3d-gen', label: '3D Gen' },
  { id: 'multi-modal', label: 'Multi-Modal' },
]

// The Store is apps. Every model -- the LLMs agents run on and the media
// models they call as tools -- lives on the Agents page.
function getSectionId(recipe) {
  if ((recipe.source || 'community') === 'spark-ai-hub') return 'spark-ai-hub'
  return 'official'
}

export default function Catalog({ search = '' }) {
  const recipes = useStore((s) => s.recipes)
  const hidden = useStore((s) => s.hidden)
  const showHidden = useStore((s) => s.showHidden)
  const setShowHidden = useStore((s) => s.setShowHidden)
  const [category, setCategory] = useState('all')

  const matches = useMemo(() => (r) => {
    const recipeCategories = Array.isArray(r.categories) && r.categories.length > 0
      ? r.categories
      : [r.category]
    if (category !== 'all' && !recipeCategories.includes(category)) return false
    if (search) {
      const q = search.toLowerCase()
      if (!r.name.toLowerCase().includes(q) && !r.tags.some((t) => t.includes(q))) return false
    }
    return true
  }, [category, search])

  const filtered = useMemo(
    () => recipes.filter((r) => !isModel(r) && matches(r) && (showHidden || !hidden.has(r.slug))),
    [recipes, matches, showHidden, hidden],
  )
  // A search for "qwen" here should not come back empty just because the
  // Qwen models moved: say how many are waiting on the Agents page. The search
  // box is shared, so following the link keeps the query.
  const agentHits = useMemo(
    () => (search ? recipes.filter((r) => isModel(r) && matches(r)).length : 0),
    [recipes, matches, search],
  )

  const shelves = useMemo(() => {
    const pick = (id) => filtered.filter((r) => getSectionId(r) === id)
    return {
      spark: pick('spark-ai-hub').sort(byRelease),
      official: pick('official').sort(byRelease),
    }
  }, [filtered])

  const isBrowsing = !search && category === 'all'

  return (
    <div className="pb-14">
      {/* ─── Category filters ─── */}
      <div className={`flex overflow-x-auto px-6 pb-2 pt-6`}>
        <ToggleGroup
          type="single"
          value={category}
          onValueChange={(next) => { if (next) setCategory(next) }}
          variant="outline"
          size="sm"
          className="shrink-0"
        >
          {CATEGORIES.map((c) => (
            <ToggleGroupItem key={c.id} value={c.id} className="whitespace-nowrap data-[state=on]:border-primary/40 data-[state=on]:bg-primary/10 data-[state=on]:text-primary">
              {c.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </div>

      {hidden.size > 0 && (
        <div className="px-6 pt-2">
          <Button variant="outline" size="sm" onClick={() => setShowHidden(!showHidden)}>
            {showHidden ? `Hide hidden (${hidden.size})` : `Show hidden (${hidden.size})`}
          </Button>
        </div>
      )}

      {agentHits > 0 && (
        <div className="px-6 pt-3">
          <Button
            asChild
            variant="outline"
            size="sm"
            className="gap-2 border-primary/30 text-primary hover:text-primary"
          >
            <Link to="/agents">
              {agentHits} matching model{agentHits > 1 ? 's' : ''} for agents
              <span aria-hidden>→</span>
            </Link>
          </Button>
        </div>
      )}

      {/* Search and category filters collapse the shelves into a plain grid —
          scanning results sideways is worse than scanning them down. */}
      {!isBrowsing ? (
        <ResultsGrid recipes={filtered} search={search} />
      ) : (
        <div className="space-y-6 pt-5">
          {shelves.spark.length > 0 && (
            <CardRow title="Spark-Optimized" subtitle="Built & tested for DGX Spark" wrap>
              {shelves.spark.map((r) => <PosterCard key={r.slug} recipe={r} />)}
            </CardRow>
          )}

          {shelves.official.length > 0 && (
            <CardRow title="Official Apps" subtitle="Published by the original developers" wrap>
              {shelves.official.map((r) => <PosterCard key={r.slug} recipe={r} />)}
            </CardRow>
          )}

          {shelves.spark.length === 0 && shelves.official.length === 0 && <Empty />}
        </div>
      )}
    </div>
  )
}

function ResultsGrid({ recipes, search }) {
  const sorted = useMemo(() => [...recipes].sort(byLab), [recipes])
  if (sorted.length === 0) return <Empty />
  return (
    <div className="px-6 pt-6">
      <div className="mb-3 flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <h2 className="m-0 font-display text-base font-semibold tracking-tight text-foreground">
          {sorted.length} {sorted.length === 1 ? 'result' : 'results'}
        </h2>
        {search && <span className="text-sm text-muted-foreground">for “{search}”</span>}
      </div>
      <div className="grid gap-3" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))' }}>
        {sorted.map((r) => <RecipeCard key={r.slug} recipe={r} />)}
      </div>
    </div>
  )
}

function Empty() {
  return (
    <div className="px-6 pt-4">
      <EmptyState
        icon={<Search className="h-6 w-6" />}
        title="No apps found"
        description="Try a different search or category"
      />
    </div>
  )
}

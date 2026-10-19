import { Eye, EyeOff } from 'lucide-react'
import { cn } from 'cn'
import { useStore } from '../store'
import { Button } from '@/components/ui/button'

/* The little tick that takes a card out of the catalogue.
 *
 * It is a view preference, not a lifecycle action: a hidden recipe keeps its
 * container, its files and its place in /v1. Store and Agents share the same
 * set, so hiding something in one place hides it in the other until the Show
 * hidden switch brings it back.
 */
export default function HideButton({ slug, size = 'md', always = false, className = '' }) {
  const hidden = useStore((s) => s.hidden)
  const toggleHidden = useStore((s) => s.toggleHidden)
  const isHidden = hidden.has(slug)
  const label = isHidden ? 'Show this card again' : 'Hide this card'

  return (
    <Button
      type="button"
      variant="ghost"
      size={size === 'sm' ? 'icon-xs' : 'icon-sm'}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        toggleHidden(slug)
      }}
      title={label}
      aria-label={label}
      className={cn(
        'shrink-0 text-muted-foreground transition-colors hover:text-foreground',
        isHidden || always
          ? 'opacity-100'
          : 'opacity-0 group-hover:opacity-100 focus-visible:opacity-100',
        className,
      )}
    >
      {isHidden ? <Eye /> : <EyeOff />}
    </Button>
  )
}

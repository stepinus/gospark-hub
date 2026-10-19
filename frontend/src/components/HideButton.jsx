import { useStore } from '../store'

/* The little tick that takes a card out of the catalogue.
 *
 * It is a view preference, not a lifecycle action: a hidden recipe keeps its
 * container, its files and its place in /v1. Store and Agents share the same
 * set, so hiding something in one place hides it in the other until the Show
 * hidden switch brings it back.
 */
export default function HideButton({ slug, size = 'md', className = '' }) {
  const hidden = useStore((s) => s.hidden)
  const toggleHidden = useStore((s) => s.toggleHidden)
  const isHidden = hidden.has(slug)

  return (
    <button
      type="button"
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        toggleHidden(slug)
      }}
      title={isHidden ? 'Show this card again' : 'Hide this card'}
      aria-label={isHidden ? 'Show this card again' : 'Hide this card'}
      className={`flex shrink-0 cursor-pointer items-center justify-center rounded-full border border-outline-dim bg-surface-high/90 p-0 leading-none transition-opacity hover:text-text ${
        size === 'sm' ? 'h-5 w-5 text-[11px]' : 'h-6 w-6 text-[13px]'
      } ${
        isHidden ? 'text-primary opacity-100' : 'text-text-dim opacity-0 group-hover:opacity-100 focus:opacity-100'
      } ${className}`}
    >
      {isHidden ? '◉' : '⊘'}
    </button>
  )
}

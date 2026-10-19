import { useStore } from '../store'

/* The little tick that takes a card out of the catalogue.
 *
 * It is a view preference, not a lifecycle action: a hidden recipe keeps its
 * container, its files and its place in /v1. Store and Agents share the same
 * set, so hiding something in one place hides it in the other until the Show
 * hidden switch brings it back.
 */
export default function HideButton({ slug, className = '' }) {
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
      className={`cursor-pointer border-none bg-transparent p-0 text-[12px] leading-none transition-opacity ${
        isHidden ? 'text-primary opacity-100' : 'text-text-dim opacity-0 group-hover:opacity-100'
      } ${className}`}
    >
      {isHidden ? '◉' : '⊘'}
    </button>
  )
}

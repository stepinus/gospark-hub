import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

// A shelf of cards. By default it scrolls horizontally with arrow controls that
// hide when there is nothing further to scroll to; with `wrap` it lays every
// card out on a wrapping grid instead, so nothing is hidden off-screen.
export default function CardRow({ title, subtitle, actions, wrap = false, children }) {
  const scroller = useRef(null)
  const [atStart, setAtStart] = useState(true)
  const [atEnd, setAtEnd] = useState(true)

  const sync = useCallback(() => {
    const el = scroller.current
    if (!el) return
    setAtStart(el.scrollLeft <= 2)
    setAtEnd(el.scrollLeft + el.clientWidth >= el.scrollWidth - 2)
  }, [])

  useEffect(() => {
    const el = scroller.current
    if (!el) return
    sync()
    const ro = new ResizeObserver(sync)
    ro.observe(el)
    return () => ro.disconnect()
  }, [sync, children])

  const scrollBy = (dir) => {
    const el = scroller.current
    if (!el) return
    el.scrollBy({ left: dir * Math.max(el.clientWidth * 0.8, 200), behavior: 'smooth' })
  }

  const canScroll = !wrap && !(atStart && atEnd)

  return (
    <Card className="mx-6 gap-0 border-border bg-card p-0 shadow-none [--card-spacing:0px]">
      <div className="flex flex-wrap items-end gap-x-3 gap-y-2 px-4 pb-3 pt-3.5">
        <div className="min-w-0">
          <h2 className="m-0 font-display text-sm font-semibold tracking-tight text-foreground">{title}</h2>
          {subtitle && <p className="m-0 mt-0.5 text-xs text-muted-foreground">{subtitle}</p>}
        </div>
        {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
        {canScroll && (
          <div className={`flex items-center gap-1 ${actions ? '' : 'ml-auto'}`}>
            <ArrowButton dir="left" disabled={atStart} onClick={() => scrollBy(-1)} />
            <ArrowButton dir="right" disabled={atEnd} onClick={() => scrollBy(1)} />
          </div>
        )}
      </div>

      {wrap ? (
        <div className="flex flex-wrap gap-3 px-4 pb-4">{children}</div>
      ) : (
        <div
          ref={scroller}
          onScroll={sync}
          className="row-scroller flex gap-3 overflow-x-auto scroll-smooth px-4 pb-4"
        >
          {children}
        </div>
      )}
    </Card>
  )
}

function ArrowButton({ dir, disabled, onClick }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      onClick={onClick}
      disabled={disabled}
      aria-label={dir === 'left' ? 'Scroll left' : 'Scroll right'}
      className="text-muted-foreground hover:text-foreground"
    >
      {dir === 'left' ? <ChevronLeft /> : <ChevronRight />}
    </Button>
  )
}

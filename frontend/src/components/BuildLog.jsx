import { useEffect, useRef } from 'react'
import { useStore } from '../store'

/* The build log, pinned to the bottom of the window while an install runs.
 * A small header names the app and the state; the lines scroll underneath, and
 * the box keeps itself scrolled to the newest one. */
export default function BuildLog() {
  const installing = useStore((s) => s.installing)
  const buildLogs = useStore((s) => s.buildLogs)
  const scrollRef = useRef(null)

  // installing is a map slug->true; pick the first active slug to show logs for
  const activeSlug = Object.keys(installing)[0] || null
  const lines = activeSlug ? (buildLogs[activeSlug] || []) : []

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [lines.length])

  if (!activeSlug) return null

  return (
    <div className="fixed right-0 bottom-0 left-0 z-50 border-t border-border bg-[#0c0c10]">
      <div className="flex items-center justify-between gap-3 border-b border-white/10 px-4 py-2">
        <span className="truncate font-mono text-[11px] font-semibold tracking-[0.08em] text-foreground">
          BUILD LOG — {activeSlug}
        </span>
        <span className="shrink-0 font-mono text-[11px] text-muted-foreground">⟳ Building...</span>
      </div>
      <div ref={scrollRef} className="h-56 overflow-auto px-4 py-3 font-mono text-[11px]">
        {lines.map((l, i) => (
          <div
            key={i}
            className={`leading-6 ${
              l.includes('successfully') || l.includes('✅') ? 'text-primary' :
              l.includes('error') || l.includes('failed') ? 'text-destructive' :
              'text-muted-foreground'
            }`}
          >
            {l}
          </div>
        ))}
        <div className="text-primary animate-pulse">▋</div>
      </div>
    </div>
  )
}

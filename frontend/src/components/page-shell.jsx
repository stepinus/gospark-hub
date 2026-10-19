import * as React from 'react'
import { cn } from 'cn'

/* Shared page furniture. Every screen composes these so the hub reads as one
   product: one header rhythm, one section label, one empty state, one metric. */

export function PageHeader({ eyebrow, title, description, actions, className, children }) {
  return (
    <div className={cn('mb-7 flex flex-col gap-4 md:flex-row md:items-start md:justify-between', className)}>
      <div className="min-w-0">
        {eyebrow && <div className="eyebrow mb-2">{eyebrow}</div>}
        <h1 className="font-display text-2xl font-semibold leading-tight tracking-tight text-foreground md:text-[28px]">
          {title}
        </h1>
        {description && (
          <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">{description}</p>
        )}
        {children}
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

export function SectionTitle({ children, hint, right, className }) {
  return (
    <div className={cn('mb-3 flex items-baseline justify-between gap-3', className)}>
      <div className="flex items-baseline gap-3">
        <h2 className="font-display text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
          {children}
        </h2>
        {hint && <span className="text-xs text-muted-foreground/70">{hint}</span>}
      </div>
      {right}
    </div>
  )
}

export function EmptyState({ icon, title, description, action, className }) {
  return (
    <div className={cn('flex flex-col items-center justify-center rounded-lg border border-dashed border-border px-6 py-14 text-center', className)}>
      {icon && <div className="mb-3 text-muted-foreground/60">{icon}</div>}
      <div className="font-display text-sm font-semibold text-foreground">{title}</div>
      {description && <p className="mt-1.5 max-w-md text-xs leading-relaxed text-muted-foreground">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

/* label / value row, the standard shape of a detail pane */
export function Field({ label, value, mono, className, children }) {
  return (
    <div className={cn('flex items-baseline justify-between gap-4 py-2', className)}>
      <span className="shrink-0 text-[11px] uppercase tracking-[0.1em] text-muted-foreground">{label}</span>
      <span className={cn('min-w-0 truncate text-right text-sm text-foreground', mono && 'font-mono text-xs tabular-nums')}>
        {children ?? value}
      </span>
    </div>
  )
}

/* a number with a caption — sidebar gauges, system tiles, stat strips */
export function Stat({ label, value, hint, tone = 'default', className }) {
  return (
    <div className={cn('rounded-lg border border-border bg-card px-4 py-3', className)}>
      <div className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{label}</div>
      <div
        className={cn(
          'mt-1 font-mono text-xl font-semibold tabular-nums text-foreground',
          tone === 'primary' && 'text-primary',
          tone === 'warning' && 'text-warning',
          tone === 'error' && 'text-error',
        )}
      >
        {value}
      </div>
      {hint && <div className="mt-0.5 text-[11px] text-muted-foreground">{hint}</div>}
    </div>
  )
}

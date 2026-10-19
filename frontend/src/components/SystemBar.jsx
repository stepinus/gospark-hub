import { useStore } from '../store'
import { cn } from 'cn'
import { Progress } from '@/components/ui/progress'

/* The header's live figures: what the chip is, how much of its memory is held,
 * what the GPU is doing and how hot it is. Compact enough to sit between the
 * tray and the account menu, and it never wraps. */
export default function SystemBar() {
  const metrics = useStore((s) => s.metrics)

  const ramUsed = metrics?.ram_used_gb ?? 0
  const ramTotal = metrics?.ram_total_gb ?? 128
  const ramPct = ramTotal > 0 ? (ramUsed / ramTotal) * 100 : 0

  return (
    <div className="flex items-center gap-3 text-xs text-muted-foreground">
      <div className="flex items-center gap-1.5">
        <span className="h-1.5 w-1.5 rounded-full bg-primary" />
        <span className="hidden text-muted-foreground xl:inline">{metrics?.gpu_name || 'GB10'}</span>
      </div>
      <div className="flex items-center gap-1.5">
        <span className="hidden lg:inline">RAM</span>
        <Progress
          value={ramPct}
          className={cn(
            'h-1.5 w-16',
            ramPct > 80 && '[&>[data-slot=progress-indicator]]:bg-error',
          )}
        />
        <span className="hidden font-mono tabular-nums lg:inline">{ramUsed}/{ramTotal}GB</span>
      </div>
      <div className="flex items-center gap-1">
        <span className="hidden lg:inline">GPU</span>
        <span className="font-mono font-medium tabular-nums text-primary">{metrics?.gpu_utilization ?? 0}%</span>
      </div>
      {metrics?.gpu_temperature > 0 && (
        <div className="hidden items-center gap-1 xl:flex">
          <span
            className={cn(
              'font-mono tabular-nums',
              metrics.gpu_temperature > 80 ? 'text-error' : 'text-primary',
            )}
          >
            {metrics.gpu_temperature}°C
          </span>
        </div>
      )}
    </div>
  )
}

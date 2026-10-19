import { useState, useEffect } from 'react'
import { Activity } from 'lucide-react'
import { useStore } from '../store'
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'
import { PageHeader, EmptyState } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

/* One metric tile: the figure it reports, the lane it fills, and the two
 * figures that matter as much (used and free) as inset rows underneath.
 *
 * `pair`: two figures that matter equally (used and free), shown big side by
 * side under the lane instead of one of them hiding in the small print. */
function CircularGauge({ value, max, label, sublabel, unit, pair, color = 'var(--tertiary)' }) {
  const pct = max > 0 ? Math.min((value / max) * 100, 100) : 0
  const displayVal = typeof value === 'number' ? (Number.isInteger(value) ? value : value.toFixed(1)) : value

  return (
    <Card className="gap-0 p-4 shadow-none">
      <div className="flex items-center justify-between gap-2">
        <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
        <span className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: color }} />
      </div>

      <div className="mt-1 flex items-baseline gap-1">
        <span className="font-mono text-xl font-semibold tabular-nums text-foreground">{displayVal}</span>
        <span className="text-[11px] text-muted-foreground">{unit}</span>
      </div>

      <Progress value={pct} className="mt-2.5" />

      {sublabel && <p className="m-0 mt-2 text-[11px] leading-relaxed text-muted-foreground">{sublabel}</p>}

      {pair && (
        <div className="mt-3 flex flex-col gap-1">
          {pair.map((p) => (
            <div key={p.label} className="flex items-baseline justify-between gap-2 rounded-md bg-muted px-2 py-1">
              <span className="text-[10px] uppercase tracking-[0.1em] text-muted-foreground">{p.label}</span>
              <span
                className="font-mono text-sm font-semibold tabular-nums"
                style={{ color: p.color || 'var(--text)' }}
              >
                {p.value}
              </span>
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}

export default function System() {
  const metrics = useStore((s) => s.metrics)
  const recipes = useStore((s) => s.recipes)
  const theme = useStore((s) => s.theme)
  const [history, setHistory] = useState([])

  const runningApps = recipes.filter((r) => r.running || r.starting)

  useEffect(() => {
    if (!metrics) return
    // The 60-sample history is kept exactly as it was: each daemon push appends
    // one point. react-hooks/set-state-in-effect is pre-existing repo-wide
    // (the same error reports on this file before the shadcn migration).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setHistory((prev) => {
      const next = [
        ...prev,
        {
          time: new Date().toLocaleTimeString([], { minute: '2-digit', second: '2-digit' }),
          gpu: metrics.gpu_utilization,
          temp: metrics.gpu_temperature,
        },
      ]
      return next.slice(-60)
    })
  }, [metrics])

  if (!metrics) {
    return (
      <div className="page">
        <EmptyState
          icon={<Activity className="h-6 w-6" />}
          title="Waiting for metrics..."
        />
      </div>
    )
  }

  const ramPct = metrics.ram_total_gb > 0 ? (metrics.ram_used_gb / metrics.ram_total_gb) * 100 : 0
  const gridColor = theme === 'light' ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.04)'
  const axisColor = theme === 'light' ? '#8a8a9a' : '#6E6C7A'
  const tooltipBg = theme === 'light' ? '#ffffff' : '#161625'
  const tooltipBorder = theme === 'light' ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.08)'
  // How far the GPU is from slowing down is the headroom nvidia-smi reports
  // (T.Limit), measured where the GPU itself measures it -- not a fixed
  // number of degrees: under load it throttled at 83 °C with 3 °C left, so
  // the lane fills toward that margin running out, not toward 100 °C.
  const headroom = metrics.gpu_temp_headroom
  const gpuLimit = headroom != null ? metrics.gpu_temperature + Math.max(headroom, 0) : null
  const tempColor = metrics.gpu_thermal_slowdown || (headroom != null && headroom < 5)
    ? 'var(--error)'
    : headroom != null && headroom < 15 ? 'var(--warning)' : 'var(--tertiary)'
  const ramFree = metrics.ram_total_gb - metrics.ram_used_gb
  const size = (gb) => (gb >= 1000 ? `${(gb / 1024).toFixed(1)} TB` : `${Math.round(gb)} GB`)
  const freeColor = (free, total) => (free / total < 0.1 ? 'var(--error)' : free / total < 0.25 ? 'var(--warning)' : 'var(--primary)')

  return (
    <div className="page flex flex-col gap-6">
      <PageHeader
        title="System Monitor"
        description={`${metrics.gpu_name || 'NVIDIA DGX Spark'} — ${metrics.ram_total_gb} GB Unified Memory`}
      />

      {/* Metric tiles */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <CircularGauge
          value={metrics.gpu_utilization} max={100}
          label="GPU" sublabel={metrics.gpu_name || 'Blackwell'}
          unit="%" color="var(--tertiary)"
        />
        <CircularGauge
          value={Math.round(ramPct)} max={100}
          label="Memory" sublabel={`${Math.round(metrics.ram_total_gb)} GB shared by CPU and GPU`}
          unit="% used" color="var(--tertiary)"
          pair={[
            { value: `${Math.round(metrics.ram_used_gb)} GB`, label: 'used' },
            { value: `${Math.round(ramFree)} GB`, label: 'free', color: freeColor(ramFree, metrics.ram_total_gb) },
          ]}
        />
        <CircularGauge
          value={Math.round((metrics.disk_used_gb / metrics.disk_total_gb) * 100)} max={100}
          label="Disk" sublabel={`${(metrics.disk_total_gb / 1024).toFixed(1)} TB`}
          unit="% used" color="var(--primary)"
          pair={[
            { value: size(metrics.disk_used_gb), label: 'used' },
            { value: size(metrics.disk_free_gb), label: 'free', color: freeColor(metrics.disk_free_gb, metrics.disk_total_gb) },
          ]}
        />
        <CircularGauge
          value={metrics.gpu_temperature} max={gpuLimit || 100}
          label="Temperature"
          sublabel={metrics.gpu_thermal_slowdown
            ? 'GPU slowing down: too hot'
            : metrics.soc_critical ? `The chip powers off at ${metrics.soc_critical} °C` : null}
          unit="°C GPU" color={tempColor}
          pair={[
            { value: headroom != null ? `${headroom} °C` : '—', label: 'to limit', color: tempColor },
            ...(metrics.soc_temperature != null
              ? [{ value: `${metrics.soc_temperature} °C`, label: 'chip' }]
              : []),
          ]}
        />
      </div>

      {/* Performance Chart */}
      {history.length > 2 && (
        <Card className="gap-0 p-5 shadow-none">
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            <div>
              <h3 className="m-0 font-display text-sm font-semibold text-foreground">Performance History</h3>
              <p className="m-0 mt-0.5 text-[11px] text-muted-foreground">Last 60 seconds</p>
            </div>
            <div className="flex items-center gap-3">
              <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <span className="h-0.5 w-3 rounded-full bg-tertiary" /> GPU %
              </span>
              <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <span className="h-0.5 w-3 rounded-full bg-warning" /> Temp °C
              </span>
            </div>
          </div>
          <ResponsiveContainer width="100%" height={220}>
            <LineChart data={history}>
              <CartesianGrid strokeDasharray="3 3" stroke={gridColor} />
              <XAxis dataKey="time" stroke={axisColor} tick={{ fontSize: 9, fontFamily: 'Space Grotesk' }} />
              <YAxis stroke={axisColor} tick={{ fontSize: 9, fontFamily: 'Space Grotesk' }} />
              <Tooltip
                contentStyle={{
                  background: tooltipBg,
                  border: `1px solid ${tooltipBorder}`,
                  borderRadius: 8,
                  fontFamily: 'Space Grotesk',
                  fontSize: 12,
                }}
                labelStyle={{ color: axisColor }}
              />
              <Line type="monotone" dataKey="gpu" stroke="var(--tertiary)" strokeWidth={2} dot={false} name="GPU %" />
              <Line type="monotone" dataKey="temp" stroke="#FBBF24" strokeWidth={2} dot={false} name="Temp °C" />
            </LineChart>
          </ResponsiveContainer>
        </Card>
      )}

      {/* Running Processes */}
      {runningApps.length > 0 && (
        <Card className="gap-0 p-5 shadow-none">
          <h3 className="m-0 mb-3 font-display text-sm font-semibold text-foreground">Active Processes</h3>
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-8 pl-0 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                  App
                </TableHead>
                <TableHead className="hidden h-8 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground sm:table-cell">
                  Port
                </TableHead>
                <TableHead className="h-8 pr-0 text-right text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                  Status
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runningApps.map((r) => (
                <TableRow key={r.slug}>
                  <TableCell className="pl-0">
                    <span className="flex min-w-0 items-center gap-2">
                      {r.logo ? (
                        <img
                          src={r.logo}
                          alt=""
                          className="h-6 w-6 shrink-0 rounded-md bg-muted object-contain p-0.5"
                        />
                      ) : (
                        <span className="text-sm text-foreground">{r.icon || '◻'}</span>
                      )}
                      <span className="truncate font-medium text-foreground">{r.name}</span>
                    </span>
                  </TableCell>
                  <TableCell className="hidden text-right font-mono text-xs tabular-nums text-muted-foreground sm:table-cell">
                    {r.app_url ? `/run/${r.slug}/` : (r.ui?.port || '—')}
                  </TableCell>
                  <TableCell className="pr-0 text-right">
                    <Badge
                      variant="outline"
                      className={
                        r.ready
                          ? 'gap-1.5 border-success/30 bg-success/10 text-success'
                          : 'gap-1.5 border-warning/30 bg-warning/10 text-warning'
                      }
                    >
                      <span className={`h-1.5 w-1.5 rounded-full ${r.ready ? 'bg-success' : 'bg-warning'}`} />
                      {r.ready ? 'Running' : 'Starting'}
                    </Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  )
}

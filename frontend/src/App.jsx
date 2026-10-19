import { useEffect, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import {
  Bot, Cpu, ExternalLink, FolderOpen, Info, LayoutGrid, PanelLeftClose, PanelLeftOpen,
  Play, RefreshCw, Search, SlidersHorizontal, Users,
} from 'lucide-react'
import { useStore } from './store'
import { useMetrics } from './hooks/useMetrics'
import ThemeToggle from './components/ThemeToggle'
import RunningTray from './components/RunningTray'
import HfTokenModal from './components/HfTokenModal'
import HfAccessModal from './components/HfAccessModal'
import LaunchConflictModal from './components/LaunchConflictModal'
import { useAuth } from './auth'
import Account, { Avatar } from './pages/Account'
import UsersPage from './pages/Users'
import Files from './pages/Files'
import Catalog from './pages/Catalog'
import Agents from './pages/Agents'
import Running from './pages/Running'
import System from './pages/System'
import Settings from './pages/Settings'
import Engines from './pages/Engines'
import LlamaSwap from './pages/LlamaSwap'
import RecipeDetail from './pages/RecipeDetail'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { PageHeader, SectionTitle } from '@/components/page-shell'

const NAV_ITEMS = [
  { path: '/', label: 'Store', icon: LayoutGrid },
  { path: '/agents', label: 'Agents', icon: Bot },
  { path: '/running', label: 'Running', icon: Play },
  { path: '/files', label: 'My files', icon: FolderOpen },
  { path: '/about', label: 'About', icon: Info },
]

// Only an admin can reach /users and /settings; everything else is open to any
// active account, so these are the nav items that are conditional.
const ADMIN_NAV_ITEMS = [
  { path: '/users', label: 'Users', icon: Users },
  { path: '/llamaswap', label: 'llama-swap', icon: RefreshCw },
  { path: '/engines', label: 'Engines', icon: Cpu },
  { path: '/settings', label: 'Settings', icon: SlidersHorizontal },
]

const PAGE_TITLES = {
  '/': 'Store',
  '/agents': 'Agents',
  '/running': 'Running',
  '/system': 'System',
  '/about': 'About',
  '/account': 'Account',
  '/users': 'Users',
  '/llamaswap': 'llama-swap',
  '/engines': 'Engines',
  '/settings': 'Settings',
  '/files': 'My files',
  '/upload': 'My files',
}

export default function App() {
  const recipes = useStore((s) => s.recipes)
  const fetchRecipes = useStore((s) => s.fetchRecipes)
  const setNavigate = useStore((s) => s.setNavigate)
  const theme = useStore((s) => s.theme)
  const metrics = useStore((s) => s.metrics)
  const user = useAuth((s) => s.user)
  const [search, setSearch] = useState('')
  const [wide, setWide] = useState(() => {
    try { return localStorage.getItem('spark-ai-hub-sidebar') === 'wide' } catch { return false }
  })
  const toggleWide = () => {
    setWide(!wide)
    try { localStorage.setItem('spark-ai-hub-sidebar', wide ? 'narrow' : 'wide') } catch { /* kept for this visit only */ }
  }
  const location = useLocation()
  const navigate = useNavigate()

  const isDetail = location.pathname.startsWith('/app/')

  useMetrics()

  // Let store actions (selectRecipe, …) drive the router.
  useEffect(() => {
    setNavigate(navigate)
  }, [navigate, setNavigate])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    document.documentElement.classList.toggle('dark', theme !== 'light')
  }, [theme])

  // Distinct per-tab titles so multiple browser tabs stay tellable apart.
  useEffect(() => {
    const slug = isDetail ? decodeURIComponent(location.pathname.slice('/app/'.length)) : null
    const name = slug ? (recipes.find((r) => r.slug === slug)?.name || slug) : PAGE_TITLES[location.pathname]
    document.title = name ? `${name} · Spark AI Hub` : 'Spark AI Hub'
  }, [location.pathname, isDetail, recipes])

  useEffect(() => {
    fetchRecipes()
    const interval = setInterval(fetchRecipes, 5000)
    return () => clearInterval(interval)
  }, [fetchRecipes])

  const navItems = user?.role === 'admin' ? [...NAV_ITEMS, ...ADMIN_NAV_ITEMS] : NAV_ITEMS
  const searchPlaceholder = location.pathname === '/agents' ? 'Search models…' : 'Search apps…'

  // Phones get the same app with the chrome moved: the sidebar becomes a tab
  // bar along the bottom (where a thumb reaches), and the header stacks the
  // running tray under the title instead of beside it.
  return (
    <TooltipProvider delayDuration={250}>
      <div className="flex h-[100dvh] overflow-hidden bg-background text-foreground">
        {/* ─── Rail (tablet and up): icons, or icons with their names on a
             laptop-wide window. ─── */}
        <aside
          className={`hidden shrink-0 flex-col border-r border-sidebar-border bg-sidebar py-3 transition-[width] duration-200 md:flex ${
            wide ? 'w-[212px] px-3' : 'w-[68px] px-2.5'
          }`}
        >
          <Link
            to="/"
            title="Spark AI Hub"
            className={`mb-5 flex h-10 items-center gap-2.5 rounded-md px-1 no-underline text-foreground ${wide ? '' : 'justify-center'}`}
          >
            <img src="/brand/spark-ai-hub-mark.svg" alt="" className="h-8 w-8 rounded-md border border-sidebar-border bg-[#101a07] p-1" />
            {wide && <span className="font-display text-sm font-semibold tracking-tight">Spark AI Hub</span>}
          </Link>

          <nav className="flex flex-1 flex-col gap-0.5">
            {navItems.map((item) => {
              const isActive = location.pathname === item.path
              const Icon = item.icon
              return (
                <Tooltip key={item.path}>
                  <TooltipTrigger asChild>
                    <Link
                      to={item.path}
                      className={`group relative flex h-9 items-center rounded-md text-[13px] font-medium no-underline transition-colors ${
                        wide ? 'gap-2.5 px-2.5' : 'justify-center'
                      } ${
                        isActive
                          ? 'bg-sidebar-accent text-foreground'
                          : 'text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground'
                      }`}
                    >
                      {isActive && <span className="absolute left-0 top-1.5 h-6 w-[2px] rounded-full bg-primary" />}
                      <Icon className={`h-[17px] w-[17px] shrink-0 ${isActive ? 'text-primary' : ''}`} />
                      {wide && <span className="truncate">{item.label}</span>}
                    </Link>
                  </TooltipTrigger>
                  <TooltipContent side="right">{item.label}</TooltipContent>
                </Tooltip>
              )
            })}
          </nav>

          {/* Bottom: account, live gauges, theme, collapse */}
          <div className="mt-4 flex flex-col gap-3">
            <div className={`flex flex-col gap-2 ${wide ? 'rounded-md border border-sidebar-border p-2.5' : 'items-center'}`}>
              {metrics && (
                <div className={`grid gap-1.5 ${wide ? 'grid-cols-2' : 'grid-cols-1'}`}>
                  <MiniGauge value={metrics.gpu_utilization} label="GPU" onClick={() => navigate('/system')} />
                  {/* The ring fills with what is taken; the number is what is left. */}
                  <MiniGauge
                    value={metrics.ram_total_gb > 0 ? Math.round((metrics.ram_used_gb / metrics.ram_total_gb) * 100) : 0}
                    text={`${Math.round(metrics.ram_total_gb - metrics.ram_used_gb)}G`}
                    title={`${Math.round(metrics.ram_total_gb - metrics.ram_used_gb)} GB free of ${Math.round(metrics.ram_total_gb)} GB`}
                    label="FREE"
                    onClick={() => navigate('/system')}
                  />
                  <MiniGauge
                    value={metrics.disk_total_gb > 0 ? Math.round((metrics.disk_used_gb / metrics.disk_total_gb) * 100) : 0}
                    label="DSK"
                    onClick={() => navigate('/system')}
                  />
                  {/* The ring fills toward the GPU's own limit, not toward 100 °C. */}
                  <MiniGauge
                    value={metrics.gpu_temp_headroom != null
                      ? Math.round((metrics.gpu_temperature / (metrics.gpu_temperature + Math.max(metrics.gpu_temp_headroom, 0))) * 100)
                      : Math.round(metrics.gpu_temperature)}
                    text={`${Math.round(metrics.gpu_temperature)}°`}
                    title={metrics.gpu_temp_headroom != null
                      ? `GPU ${metrics.gpu_temperature} °C, ${metrics.gpu_temp_headroom} °C below its limit`
                      : `GPU ${metrics.gpu_temperature} °C`}
                    label="TMP"
                    onClick={() => navigate('/system')}
                    tone={metrics.gpu_thermal_slowdown || (metrics.gpu_temp_headroom ?? 99) < 5 ? 'error' : (metrics.gpu_temp_headroom ?? 99) < 15 ? 'warning' : 'default'}
                  />
                </div>
              )}
            </div>

            {user && (
              <Link
                to="/account"
                title={`${user.name || user.email} — account & API key`}
                className={`flex min-w-0 items-center gap-2.5 rounded-md p-1 no-underline text-foreground transition-colors hover:bg-sidebar-accent/60 ${
                  location.pathname === '/account' ? 'bg-sidebar-accent' : ''
                } ${wide ? '' : 'justify-center'}`}
              >
                <Avatar user={user} size={30} />
                {wide && <span className="truncate text-xs font-medium">{user.name || user.email}</span>}
              </Link>
            )}

            <div className={`flex items-center gap-1.5 ${wide ? 'justify-between' : 'flex-col'}`}>
              <ThemeToggle />
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={toggleWide}
                    className="hidden h-8 w-8 text-muted-foreground hover:text-foreground lg:inline-flex"
                  >
                    {wide ? <PanelLeftClose className="h-4 w-4" /> : <PanelLeftOpen className="h-4 w-4" />}
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="right">{wide ? 'Collapse names' : 'Show names'}</TooltipContent>
              </Tooltip>
            </div>
          </div>
        </aside>

        {/* ─── Main ─── */}
        <div className="flex min-w-0 flex-1 flex-col">
          <header className="shrink-0 border-b border-border bg-background/85 backdrop-blur-md">
            <div className="flex h-14 items-center gap-3 px-4 md:px-6">
              <Link to="/" className="flex shrink-0 items-center gap-2 no-underline text-foreground md:hidden">
                <img src="/brand/spark-ai-hub-mark.svg" alt="" className="h-7 w-7 rounded-md border border-border bg-[#101a07] p-1" />
              </Link>
              <span className="hidden font-display text-sm font-semibold tracking-tight md:inline">Spark AI Hub</span>
              <Badge variant="outline" className="hidden font-mono text-[10px] text-muted-foreground sm:inline-flex">v0.1</Badge>

              {/* What is running, always in view. */}
              <div className="mx-2 hidden min-w-0 flex-1 md:flex">
                <RunningTray />
              </div>

              <div className="ml-auto flex shrink-0 items-center gap-2 md:ml-0">
                {!isDetail && (
                  <div className="relative">
                    <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      type="search"
                      placeholder={searchPlaceholder}
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                      className="h-8 w-36 pl-8 text-xs sm:w-56 lg:w-64"
                    />
                  </div>
                )}
                <div className="md:hidden"><ThemeToggle /></div>
              </div>
            </div>

            {/* Phones: the tray gets its own scrolling row under the title. */}
            <div className="row-scroller overflow-x-auto px-4 pb-2.5 md:hidden">
              <RunningTray scroll />
            </div>
          </header>

          {/* Content */}
          <main className="flex-1 overflow-y-auto overflow-x-hidden pb-20 md:pb-0">
            <Routes>
              <Route path="/" element={<Catalog search={search} />} />
              <Route path="/agents" element={<Agents search={search} />} />
              <Route path="/running" element={<Running />} />
              <Route path="/system" element={<System />} />
              <Route path="/about" element={<About />} />
              <Route path="/account" element={<Account />} />
              <Route path="/users" element={<UsersPage />} />
              <Route path="/settings" element={<Settings />} />
              <Route path="/llamaswap" element={<LlamaSwap />} />
              <Route path="/engines" element={<Engines />} />
              <Route path="/files" element={<Files />} />
              <Route path="/upload" element={<Navigate to="/files" replace />} />
              <Route path="/app/:slug" element={<RecipeDetail />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </main>
        </div>

        {/* ─── Tab bar (phones) ─── */}
        <nav className="fixed inset-x-0 bottom-0 z-40 flex items-stretch justify-around border-t border-border bg-sidebar/95 pb-[env(safe-area-inset-bottom)] backdrop-blur-md md:hidden">
          {navItems.map((item) => {
            const isActive = location.pathname === item.path
            const Icon = item.icon
            return (
              <Link
                key={item.path}
                to={item.path}
                className={`flex min-w-0 flex-1 flex-col items-center gap-1 py-2 text-[10px] font-medium no-underline ${
                  isActive ? 'text-primary' : 'text-muted-foreground'
                }`}
              >
                <Icon className="h-[18px] w-[18px]" />
                <span className="truncate">{item.label}</span>
              </Link>
            )
          })}
          {user && (
            <Link
              to="/account"
              className={`flex min-w-0 flex-1 flex-col items-center gap-1 py-2 text-[10px] font-medium no-underline ${
                location.pathname === '/account' ? 'text-primary' : 'text-muted-foreground'
              }`}
            >
              <Avatar user={user} size={18} />
              <span className="truncate">Account</span>
            </Link>
          )}
        </nav>
        <HfTokenModal />
        <HfAccessModal />
        <LaunchConflictModal />
      </div>
    </TooltipProvider>
  )
}

/* ─── About ─── */
function About() {
  return (
    <div className="page mx-auto max-w-2xl">
      <PageHeader
        eyebrow="About"
        title="Spark AI Hub"
        description="One-click AI app launcher for NVIDIA DGX Spark."
      />

      <div className="flex flex-col gap-4">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">What this is</CardTitle>
          </CardHeader>
          <CardContent className="text-sm leading-relaxed text-muted-foreground">
            Spark AI Hub lets you install, run, and manage GPU-accelerated AI applications on your
            NVIDIA DGX Spark — all from a single web interface. No terminal required.
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Links</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col divide-y divide-border p-0">
            <a
              href="https://github.com/WaxacaBytes/spark-ai-hub"
              target="_blank"
              rel="noreferrer"
              className="flex items-center gap-3 px-5 py-3 text-sm text-foreground no-underline transition-colors hover:bg-muted/60"
            >
              <GithubGlyph className="h-4 w-4 text-muted-foreground" />
              <span>GitHub repository</span>
              <ExternalLink className="ml-auto h-3.5 w-3.5 text-muted-foreground" />
            </a>
            <a
              href="https://github.com/WaxacaBytes/spark-ai-hub/issues"
              target="_blank"
              rel="noreferrer"
              className="flex items-center gap-3 px-5 py-3 text-sm text-foreground no-underline transition-colors hover:bg-muted/60"
            >
              <Info className="h-4 w-4 text-muted-foreground" />
              <span>Report an issue</span>
              <ExternalLink className="ml-auto h-3.5 w-3.5 text-muted-foreground" />
            </a>
          </CardContent>
        </Card>

        <p className="text-center text-xs text-muted-foreground">
          Made by{' '}
          <a href="https://github.com/WaxacaBytes" target="_blank" rel="noreferrer" className="font-medium text-foreground no-underline hover:text-primary">
            WaxacaBytes
          </a>
        </p>
      </div>
    </div>
  )
}

/* ─── Mini gauge for the rail ─── */
function MiniGauge({ value, label, text, title, tone = 'default', onClick }) {
  const radius = 13
  const stroke = 2.5
  const circ = 2 * Math.PI * radius
  const pct = Math.min(Math.max(value, 0), 100)
  const offset = circ - (pct / 100) * circ
  const color = tone === 'error' ? 'var(--error)' : tone === 'warning' ? 'var(--warning)' : 'var(--tertiary)'

  return (
    <button
      type="button"
      onClick={onClick}
      title={`${title || `${label}: ${Math.round(value)}%`} — open System Monitor`}
      className="flex items-center gap-2 rounded-md border-none bg-transparent p-0 text-left transition-opacity hover:opacity-80"
    >
      <svg width="30" height="30" viewBox="0 0 30 30" className="shrink-0">
        <circle cx="15" cy="15" r={radius} fill="none" stroke="var(--outline)" strokeWidth={stroke} />
        <circle
          cx="15" cy="15" r={radius} fill="none"
          stroke={color} strokeWidth={stroke}
          strokeDasharray={circ} strokeDashoffset={offset}
          strokeLinecap="round"
          transform="rotate(-90 15 15)"
          className="gauge-ring"
        />
      </svg>
      <span className="flex min-w-0 flex-col leading-tight">
        <span className="text-[9px] uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
        <span className="font-mono text-[11px] font-semibold tabular-nums text-foreground">
          {text ?? `${Math.round(value)}%`}
        </span>
      </span>
    </button>
  )
}

function GithubGlyph({ className }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-hidden>
      <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z" />
    </svg>
  )
}

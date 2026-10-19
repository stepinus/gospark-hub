import { useCallback, useEffect, useMemo, useState } from 'react'
import { useAuth } from '../auth'
import {
  getLlamaswap,
  getLlamaswapConfig,
  installLlamaswap,
  measureLlamaswapModel,
  saveLlamaswapConfig,
  saveLlamaswapGroups,
  saveLlamaswapSettings,
  startLlamaswap,
  stopLlamaswap,
  updateLlamaswap,
} from '../lib/api'
import YamlEditor from '../components/YamlEditor'
import CopyField from '../components/CopyField'
import AddressPicker, { hostOptions } from '../components/AddressPicker'
import { PageHeader, SectionTitle } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Progress } from '@/components/ui/progress'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

/* llama-swap, as a sidecar of the Hub.
 *
 * llama-swap keeps models warm behind one OpenAI address and swaps them on
 * demand. The Hub owns the whole thing: it downloads the upstream release,
 * runs it as a systemd --user unit, and this page edits the YAML it serves.
 * The groups board is the point — dragging a model between groups changes
 * which models evict each other, and the memory row tells you what a group
 * will cost once its members have been measured.
 *
 * Three conveniences keep a long catalogue usable: a search box that filters
 * by name or id, a per-card hide that is remembered in the browser, and the
 * Ungrouped column, which is both a drop target and what the × button on a
 * card does — a model can always be taken out of a group again.
 */

export default function LlamaSwap() {
  const user = useAuth((s) => s.user)
  const isAdmin = !user || user.role === 'admin'

  const [state, setState] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(null)
  const [port, setPort] = useState('')
  const [yaml, setYaml] = useState('')
  const [editing, setEditing] = useState(false)
  const [notice, setNotice] = useState('')
  const [dragging, setDragging] = useState(null)
  const [measuring, setMeasuring] = useState({})
  const [query, setQuery] = useState('')
  // Where this Spark is reachable from elsewhere: the daemon reads mDNS,
  // Tailscale and the routing table, so the address offered here is not the
  // 127.0.0.1 the page happens to be open on.
  const [connect, setConnect] = useState(null)
  const [address, setAddress] = useState('')

  useEffect(() => {
    if (!isAdmin) return
    ;(async () => {
      const res = await fetch('/api/system/connect')
      if (res.ok) setConnect(await res.json())
    })()
  }, [isAdmin])

  const load = useCallback(async () => {
    const res = await getLlamaswap()
    if (!res.ok) {
      setError(res.error)
      return
    }
    setState(res.data)
    setPort(String(res.data?.port || ''))
    setError('')
  }, [])

  useEffect(() => {
    if (!isAdmin) return
    // Kick the first read outside the effect's own render pass, the way the
    // other admin pages do: setState inside the effect body is a lint error.
    ;(async () => { await load() })()
    const timer = setInterval(load, 15000)
    return () => clearInterval(timer)
  }, [isAdmin, load])

  const act = async (name, fn, message) => {
    setBusy(name)
    setNotice('')
    const res = await fn()
    setBusy(null)
    if (!res.ok) {
      setError(res.error)
      return
    }
    if (message) setNotice(message)
    await load()
  }

  const openConfig = async () => {
    const res = await getLlamaswapConfig()
    if (!res.ok) {
      setError(res.error)
      return
    }
    setYaml(res.data?.yaml || '')
    setEditing(true)
  }

  const saveConfig = async () => {
    setBusy('config')
    const res = await saveLlamaswapConfig(yaml)
    setBusy(null)
    if (!res.ok) {
      setError(res.error)
      return
    }
    setError('')
    setNotice('Config saved; llama-swap reloads it in place.')
    setEditing(false)
    await load()
  }

  // Drag and drop: a model belongs to the group it is dropped in and no other,
  // which is how llama-swap's own config models it. Dropping on Ungrouped — or
  // pressing × on a card — removes it from every group.
  const moveModel = async (model, groupName) => {
    const groups = state?.config?.groups || {}
    const next = {}
    for (const [name, group] of Object.entries(groups)) {
      const members = (group.members || []).filter((m) => m !== model)
      if (name === groupName) members.push(model)
      next[name] = { ...group, members }
    }
    if (groupName && !next[groupName]) {
      next[groupName] = { ...emptyGroup(), members: [model] }
    }
    setBusy('groups')
    const res = await saveLlamaswapGroups(next)
    setBusy(null)
    if (!res.ok) {
      setError(res.error)
      return
    }
    setError('')
    await load()
  }

  const addGroup = async () => {
    const name = window.prompt('New group name')?.trim()
    if (!name) return
    const groups = { ...(state?.config?.groups || {}) }
    if (groups[name]) return
    groups[name] = emptyGroup()
    setBusy('groups')
    const res = await saveLlamaswapGroups(groups)
    setBusy(null)
    if (!res.ok) setError(res.error)
    else await load()
  }

  const measure = async (model) => {
    setMeasuring((prev) => ({ ...prev, [model]: true }))
    const res = await measureLlamaswapModel(model)
    setMeasuring((prev) => ({ ...prev, [model]: false }))
    if (!res.ok) {
      setError(res.error)
      return
    }
    setError('')
    await load()
  }

  const matches = useCallback((id, name) => {
    const q = query.trim().toLowerCase()
    if (!q) return true
    return id.toLowerCase().includes(q) || (name || '').toLowerCase().includes(q)
  }, [query])

  const isVisible = useCallback(
    (id) => matches(id, state?.config?.models?.[id]?.name || state?.recipes?.find((r) => r.slug === id)?.name),
    [matches, state],
  )

  const recipesBySlug = useMemo(() => {
    const map = new Map()
    for (const r of state?.recipes || []) map.set(r.slug, r)
    return map
  }, [state])
  const groups = useMemo(() => state?.config?.groups || {}, [state])
  const models = useMemo(() => state?.config?.models || {}, [state])
  const modelIds = useMemo(() => Object.keys(models).sort(), [models])
  // The same hosts the Hub hands out, on the sidecar's port instead of its own.
  const addresses = useMemo(() => {
    if (!state?.port) return []
    return hostOptions(connect).map((o) => ({
      ...o,
      // An IPv6 literal needs its brackets before a port.
      url: `http://${o.host.includes(':') ? `[${o.host}]` : o.host}:${state.port}/v1`,
    }))
  }, [connect, state])
  const picked = addresses.find((a) => a.url === address) || addresses[0]
  const measurements = state?.measurements || {}
  const running = useMemo(() => {
    const byId = new Map()
    for (const r of state?.running_models || []) byId.set(r.model, r)
    return byId
  }, [state])

  const groupedSet = useMemo(() => {
    const seen = new Set()
    for (const group of Object.values(groups)) for (const m of group.members || []) seen.add(m)
    return seen
  }, [groups])

  const grouped = useMemo(() => {
    const seen = new Set()
    for (const group of Object.values(groups)) for (const m of group.members || []) seen.add(m)
    return Object.keys(models).filter((id) => !seen.has(id))
  }, [groups, models])

  if (!isAdmin) {
    return (
      <div className="page">
        <p className="m-0 text-sm text-muted-foreground">Administrator rights required.</p>
      </div>
    )
  }

  const updateAvailable = Boolean(state?.installed && state?.latest && state.latest !== state.version)
  const uiURL = state?.port ? `${window.location.protocol}//${window.location.hostname}:${state.port}/ui/` : ''

  return (
    <div className="page flex flex-col gap-6">
      <PageHeader
        title="llama-swap"
        description="One address for many models: llama-swap keeps them warm and swaps on demand. The Hub installs the binary, runs it as a sidecar and edits the config here."
        actions={
          <>
            {!state?.installed && (
              <Button
                onClick={() => act('install', () => installLlamaswap(), 'Binary installed.')}
                disabled={busy !== null}
              >
                {busy === 'install' ? '⟳ Installing…' : 'Install'}
              </Button>
            )}
            {state?.installed && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => act('update', updateLlamaswap, 'Updated to the latest release.')}
                  disabled={busy !== null || !updateAvailable}
                  className="whitespace-nowrap"
                >
                  {busy === 'update' ? '⟳ Updating…' : 'Update'}
                </Button>
                {state?.running ? (
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => act('stop', stopLlamaswap, 'Sidecar stopped.')}
                    disabled={busy !== null}
                    className="whitespace-nowrap"
                  >
                    {busy === 'stop' ? '⟳ Stopping…' : 'Stop'}
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    onClick={() => act('start', startLlamaswap, 'Sidecar started.')}
                    disabled={busy !== null}
                    className="whitespace-nowrap"
                  >
                    {busy === 'start' ? '⟳ Starting…' : 'Start'}
                  </Button>
                )}
                <Button variant="outline" size="sm" asChild className="whitespace-nowrap">
                  <a href={uiURL} target="_blank" rel="noreferrer">
                    Open UI ↗
                  </a>
                </Button>
              </>
            )}
          </>
        }
      >
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Badge variant="outline" className="gap-1.5 font-normal">
            <span
              className={`h-1.5 w-1.5 shrink-0 rounded-full ${state?.running ? 'bg-success' : 'bg-muted-foreground'}`}
            />
            {state?.running ? 'Running' : state?.installed ? 'Stopped' : 'Not installed'}
          </Badge>
          {updateAvailable && (
            <Badge variant="outline" className="border-primary/30 bg-primary/10 text-primary">
              Update to {state.latest}
            </Badge>
          )}
          <span className="text-[11px] text-muted-foreground">
            {state?.installed ? `Version ${state.version}` : 'The release binary is not downloaded yet'}
            {state?.latest && !updateAvailable && state?.installed ? ' · up to date' : ''}
            {state?.sidecar_version ? ` · reported ${state.sidecar_version}` : ''}
          </span>
          {state?.config_path && (
            <span className="w-full break-all font-mono text-[11px] text-muted-foreground">
              {state.config_path}
            </span>
          )}
        </div>
      </PageHeader>

      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2">
          <p className="m-0 text-sm leading-relaxed text-destructive">{error}</p>
        </div>
      )}
      {notice && (
        <div className="rounded-lg border border-primary/30 bg-primary/10 px-3 py-2">
          <p className="m-0 text-sm leading-relaxed text-primary">{notice}</p>
        </div>
      )}

      {/* ─── Settings: the sidecar's port ─── */}
      <Card className="gap-0 p-5 shadow-none">
        <div className="flex flex-wrap items-center gap-3">
          <Label
            htmlFor="llamaswap-port"
            className="text-[10px] font-semibold uppercase tracking-[0.12em] text-muted-foreground"
          >
            Port
          </Label>
          <Input
            id="llamaswap-port"
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ''))}
            className="h-8 w-20 font-mono text-xs tabular-nums"
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => act('port', () => saveLlamaswapSettings({ port: Number(port) }), 'Port saved; restart to apply.')}
            disabled={busy !== null || !port || Number(port) === state?.port}
          >
            Save port
          </Button>
        </div>
      </Card>

      {/* ─── One-click connect ─── */}
      <Card className="gap-0 p-5 shadow-none">
        <SectionTitle
          right={
            <span className="text-[11px] text-muted-foreground">
              {state?.running ? 'What a client needs' : 'The sidecar is stopped: these work once it runs'}
            </span>
          }
        >
          Connect
        </SectionTitle>
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <CopyField
              label="API endpoint"
              value={picked?.url || `http://<address>:${state?.port || ''}/v1`}
              hint="OpenAI-compatible; the model goes in the request body"
            />
            {addresses.length > 1 && (
              <AddressPicker addresses={addresses} address={picked} onPick={setAddress} />
            )}
          </div>
          <CopyField
            label="Token"
            value={state?.api_key || 'not-needed'}
            secret={Boolean(state?.api_key)}
            hint={state?.api_key ? 'Same token for the web UI: any username, this as the password' : 'This sidecar has no apiKeys set'}
          />
        </div>
        {modelIds.length > 0 && (
          <div className="mt-4 border-t border-border pt-4">
            <span className="text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
              Models {running.size > 0 && <span className="normal-case tracking-normal">· {running.size} warm now</span>}
            </span>
            <div className="mt-2 grid gap-2 sm:grid-cols-2">
              {modelIds.map((id) => (
                <CopyField
                  key={id}
                  label={running.has(id) ? 'Model · running' : 'Model'}
                  value={id}
                  hint={models[id]?.name || undefined}
                />
              ))}
            </div>
          </div>
        )}
      </Card>

      {/* ─── YAML editor / groups board ─── */}
      <Tabs
        value={editing ? 'yaml' : 'groups'}
        onValueChange={(next) => {
          if (next === 'yaml') {
            if (!editing) openConfig()
          } else if (editing) {
            setEditing(false)
          }
        }}
        className="gap-4"
      >
        <div className="flex flex-wrap items-center justify-between gap-3">
          <TabsList variant="line">
            <TabsTrigger
              value="yaml"
              disabled={busy !== null || !state?.config_exists}
              className="px-2 text-xs font-medium"
            >
              config.yaml
            </TabsTrigger>
            <TabsTrigger value="groups" className="px-2 text-xs font-medium">
              Groups
            </TabsTrigger>
          </TabsList>
          <Button
            variant="outline"
            size="sm"
            onClick={editing ? () => setEditing(false) : openConfig}
            disabled={busy !== null || !state?.config_exists}
          >
            {editing ? 'Close editor' : 'Edit YAML'}
          </Button>
        </div>

        <TabsContent value="yaml" className="mt-0">
          <Card className="gap-0 p-5 shadow-none">
            <div className="flex items-center justify-between gap-3">
              <h2 className="m-0 font-display text-sm font-semibold text-foreground">config.yaml</h2>
              <Button size="sm" onClick={saveConfig} disabled={busy !== null}>
                {busy === 'config' ? '⟳ Saving…' : 'Save'}
              </Button>
            </div>
            <YamlEditor value={yaml} onChange={setYaml} className="mt-3 h-[28rem]" />
          </Card>
        </TabsContent>

        <TabsContent value="groups" className="mt-0">
          <div className="flex flex-col gap-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <h2 className="m-0 font-display text-sm font-semibold text-foreground">Groups</h2>
                <p className="m-0 mt-1 max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                  Drag a model into a group: members of one swap group evict each other. Drop it on
                  Ungrouped (or press ×) to take it out again.
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Filter by name or id…"
                  className="h-8 w-56 text-xs"
                />
                <Button variant="outline" size="sm" onClick={addGroup} disabled={busy !== null}>
                  + Group
                </Button>
              </div>
            </div>

            {(state?.recipes || []).length > 0 && (
              <div>
                <p className="m-0 text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                  Hub recipes · drag into a group
                </p>
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {(state.recipes || [])
                    .filter((r) => !groupedSet.has(r.slug))
                    .filter((r) => isVisible(r.slug))
                    .slice(0, 80)
                    .map((r) => (
                      <div
                        key={r.slug}
                        draggable
                        onDragStart={() => setDragging(r.slug)}
                        onDragEnd={() => setDragging(null)}
                        className="flex cursor-grab items-center gap-1.5 rounded-md border border-border bg-muted px-2 py-1"
                        title={r.slug}
                      >
                        <span className="text-[11px] text-foreground">{r.name}</span>
                        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
                          {r.memory_gb ? `${r.memory_gb} GB` : '—'}
                        </span>
                      </div>
                    ))}
                </div>
              </div>
            )}

            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
              {Object.entries(groups).map(([name, group]) => (
                <GroupColumn
                  key={name}
                  name={name}
                  group={group}
                  models={models}
                  measurements={measurements}
                  recipes={recipesBySlug}
                  memory={state?.memory}
                  running={running}
                  dragging={dragging}
                  isVisible={isVisible}
                  onDragStart={setDragging}
                  onDropModel={(model) => moveModel(model, name)}
                  onRemove={(model) => moveModel(model, '')}
                />
              ))}
              <GroupColumn
                name=""
                title="Ungrouped"
                group={{ members: grouped }}
                models={models}
                measurements={measurements}
                recipes={recipesBySlug}
                memory={state?.memory}
                running={running}
                dragging={dragging}
                isVisible={isVisible}
                onDragStart={setDragging}
                onDropModel={(model) => moveModel(model, '')}
                ungrouped
              />
            </div>
          </div>
        </TabsContent>
      </Tabs>

      {/* ─── Running models ─── */}
      <Card className="gap-0 p-5 shadow-none">
        <SectionTitle>Running now</SectionTitle>
        {(state?.running_models || []).length === 0 ? (
          <p className="m-0 text-[11px] leading-relaxed text-muted-foreground">
            Nothing is up. A request through the sidecar starts the model it names.
          </p>
        ) : (
          <div className="flex flex-col gap-2">
            {state.running_models.map((model) => {
              const measurement = measurements[model.model]
              return (
                <div key={model.model} className="rounded-lg border border-border bg-muted/40 px-3 py-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs font-semibold text-foreground">{model.name || model.model}</span>
                    <code className="font-mono text-[10px] text-muted-foreground">{model.model}</code>
                    <span className="text-[10px] text-muted-foreground">{model.state}</span>
                    <Button
                      variant="outline"
                      size="xs"
                      onClick={() => measure(model.model)}
                      disabled={Boolean(measuring[model.model])}
                      className="ml-auto"
                    >
                      {measuring[model.model] ? '⟳ Measuring…' : 'Measure'}
                    </Button>
                  </div>
                  {measurement && (
                    <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
                      <span>
                        prefill{' '}
                        <b className="font-mono font-semibold tabular-nums text-foreground">{fmt(measurement.prefill_tps)}</b>{' '}
                        tok/s
                        <span className="text-muted-foreground">
                          {' '}({measurement.prompt_tokens} tok / {measurement.ttft_ms} ms
                          {measurement.prefill_naive_tps ? `, naive ${fmt(measurement.prefill_naive_tps)}` : ''})
                        </span>
                      </span>
                      <span>
                        decode{' '}
                        <b className="font-mono font-semibold tabular-nums text-foreground">{fmt(measurement.decode_tps)}</b>{' '}
                        tok/s
                        <span className="text-muted-foreground">
                          {' '}({measurement.decode_tokens} tok / {measurement.total_ms} ms)
                        </span>
                      </span>
                      <span>
                        memory{' '}
                        <b className="font-mono font-semibold tabular-nums text-foreground">{gb(measurement.memory_bytes)}</b>
                        {measurement.memory_source ? (
                          <span className="text-muted-foreground"> · {measurement.memory_source}</span>
                        ) : null}
                      </span>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </Card>
    </div>
  )
}

function GroupColumn({
  name,
  title,
  group,
  models,
  measurements,
  recipes,
  memory,
  running,
  dragging,
  isVisible,
  onDragStart,
  onDropModel,
  onRemove,
  ungrouped,
}) {
  const members = group?.members || []
  const visible = members.filter((id) => isVisible(id))
  // Expected load: a Hub recipe knows its memory_gb, anything else falls back
  // to the last measurement. Hiding a card does not take it out of a group, so
  // the totals still count every member.
  const loads = members.map((id) => {
    const fromRecipe = recipes?.get(id)?.memory_gb
    if (fromRecipe) return fromRecipe
    const measured = measurements[id]?.memory_bytes
    return measured ? measured / (1024 ** 3) : 0
  })
  const known = loads.filter(Boolean)
  const usedGB = known.reduce((a, b) => a + b, 0)
  const unknown = members.length - known.length
  const totalGB = memory?.total_gb || 0
  const freeGB = memory?.available_gb || 0
  const fill = totalGB ? Math.min(100, (usedGB / totalGB) * 100) : 0
  // A group that wants more than the machine has free right now will evict
  // something else or fail to load: that is the number that matters, not just
  // the total.
  const overFree = freeGB > 0 && usedGB > freeGB
  const fillTone = overFree
    ? '[&_[data-slot=progress-indicator]]:bg-error'
    : fill > 70
      ? '[&_[data-slot=progress-indicator]]:bg-tertiary'
      : ''

  return (
    <Card
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault()
        if (dragging) onDropModel(dragging)
      }}
      className={`min-h-[7rem] gap-0 p-3 shadow-none ${
        ungrouped ? 'border border-dashed border-border bg-transparent ring-0' : ''
      }`}
    >
      <div className="flex items-center justify-between gap-2">
        <h3 className="m-0 text-[10px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
          {title || name}
        </h3>
        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">{members.length}</span>
      </div>
      <p className="m-0 mt-1 text-[10px] leading-relaxed text-muted-foreground">
        {usedGB > 0 ? `≈ ${usedGB.toFixed(0)} GB` : 'no memory data'}
        {totalGB ? ` of ${totalGB} GB` : ''}
        {freeGB > 0 ? ` · ${freeGB.toFixed(0)} GB free now` : ''}
        {unknown > 0 ? ` · ${unknown} unknown` : ''}
        {name && group?.swap === false ? ' · swap off' : ''}
        {name && group?.persistent ? ' · persistent' : ''}
      </p>
      {totalGB > 0 && <Progress value={fill} className={`mt-1.5 h-1.5 ${fillTone}`} />}
      <div className="mt-2 flex flex-col gap-1.5">
        {visible.map((id) => (
          <Card
            key={id}
            size="sm"
            draggable
            onDragStart={() => onDragStart(id)}
            onDragEnd={() => onDragStart(null)}
            className="group cursor-grab gap-0 px-2 py-1.5 shadow-none"
          >
            <div className="flex items-center gap-1.5">
              {running.has(id) && <span className="inline-flex h-1.5 w-1.5 shrink-0 rounded-full bg-success" />}
              <span className="truncate text-[11px] text-foreground">{models[id]?.name || id}</span>
              <div className="ml-auto flex items-center gap-1 opacity-60 transition-opacity group-hover:opacity-100">
                {!ungrouped && (
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    onClick={(e) => { e.stopPropagation(); onRemove(id) }}
                    title="Take out of the group"
                    className="text-[10px] text-muted-foreground hover:text-destructive"
                  >
                    ×
                  </Button>
                )}
              </div>
            </div>
            <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
              <code className="font-mono">{id}</code>
              {measurements[id]?.decode_tps ? <span>{fmt(measurements[id].decode_tps)} tok/s</span> : null}
              {measurements[id]?.memory_bytes
                ? <span>{gb(measurements[id].memory_bytes)}</span>
                : recipes?.get(id)?.memory_gb
                  ? <span>{recipes.get(id).memory_gb} GB</span>
                  : null}
            </div>
          </Card>
        ))}
        {visible.length === 0 && (
          <p className="m-0 py-2 text-[10px] text-muted-foreground">
            {members.length === 0 ? 'Nothing here.' : `All ${members.length} filtered out by the search.`}
          </p>
        )}
      </div>
    </Card>
  )
}

function emptyGroup() {
  return { swap: true, exclusive: true, persistent: false, members: [] }
}

function fmt(value) {
  if (value === undefined || value === null) return '—'
  return Number(value).toFixed(1)
}

function gb(bytes) {
  if (!bytes) return '—'
  return `${(Number(bytes) / (1024 ** 3)).toFixed(1)} GiB`
}

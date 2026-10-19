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
  }, [connect, state?.port])
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
    return <div className="px-6 py-20 text-center text-text-dim text-sm">Administrator rights required.</div>
  }

  const updateAvailable = Boolean(state?.installed && state?.latest && state.latest !== state.version)
  const uiURL = state?.port ? `${window.location.protocol}//${window.location.hostname}:${state.port}/ui/` : ''

  return (
    <div className="px-4 sm:px-6 py-6 pb-12 max-w-6xl mx-auto animate-fadeIn flex flex-col gap-4">
      <div className="mb-1">
        <h1 className="text-2xl font-bold tracking-tight font-display m-0">llama-swap</h1>
        <p className="text-sm text-text-dim m-0 mt-1">
          One address for many models: llama-swap keeps them warm and swaps on demand. The Hub
          installs the binary, runs it as a sidecar and edits the config here.
        </p>
      </div>

      {error && (
        <div className="rounded-2xl border border-error/30 bg-error-surface px-4 py-3">
          <p className="text-sm text-error m-0 leading-relaxed">{error}</p>
        </div>
      )}
      {notice && (
        <div className="rounded-2xl border border-primary/30 bg-primary/10 px-4 py-3">
          <p className="text-sm text-primary m-0 leading-relaxed">{notice}</p>
        </div>
      )}

      {/* ─── The sidecar itself ─── */}
      <section className="bg-surface rounded-2xl p-5">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className={`inline-flex h-2.5 w-2.5 rounded-full ${state?.running ? 'bg-primary' : 'bg-text-dim'}`} />
              <h2 className="font-semibold text-sm font-display m-0">
                {state?.running ? 'Running' : state?.installed ? 'Stopped' : 'Not installed'}
              </h2>
              {updateAvailable && (
                <span className="text-[10px] font-label font-semibold text-primary bg-primary/10 border border-primary/30 px-2 py-0.5 rounded-full">
                  Update to {state.latest}
                </span>
              )}
            </div>
            <p className="text-[11px] text-text-dim m-0 mt-1">
              {state?.installed ? `Version ${state.version}` : 'The release binary is not downloaded yet'}
              {state?.latest && !updateAvailable && state?.installed ? ' · up to date' : ''}
              {state?.sidecar_version ? ` · reported ${state.sidecar_version}` : ''}
            </p>
            {state?.config_path && (
              <p className="text-[11px] text-text-dim font-mono m-0 mt-1 break-all">{state.config_path}</p>
            )}
          </div>

          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {!state?.installed && (
              <button
                onClick={() => act('install', () => installLlamaswap(), 'Binary installed.')}
                disabled={busy !== null}
                className="btn-primary px-4 py-2 text-sm font-bold disabled:opacity-50"
              >
                {busy === 'install' ? '⟳ Installing…' : 'Install'}
              </button>
            )}
            {state?.installed && (
              <>
                <button
                  onClick={() => act('update', updateLlamaswap, 'Updated to the latest release.')}
                  disabled={busy !== null || !updateAvailable}
                  className="px-3.5 py-2 rounded-xl bg-surface-high text-text border border-outline-dim text-xs font-semibold cursor-pointer hover:bg-surface-highest disabled:opacity-50 disabled:cursor-default"
                >
                  {busy === 'update' ? '⟳ Updating…' : 'Update'}
                </button>
                {state?.running ? (
                  <button
                    onClick={() => act('stop', stopLlamaswap, 'Sidecar stopped.')}
                    disabled={busy !== null}
                    className="px-3.5 py-2 rounded-xl bg-error-surface text-error border border-error/30 text-xs font-semibold cursor-pointer hover:bg-error/15 disabled:opacity-50"
                  >
                    {busy === 'stop' ? '⟳ Stopping…' : 'Stop'}
                  </button>
                ) : (
                  <button
                    onClick={() => act('start', startLlamaswap, 'Sidecar started.')}
                    disabled={busy !== null}
                    className="btn-primary px-3.5 py-2 text-xs font-bold disabled:opacity-50"
                  >
                    {busy === 'start' ? '⟳ Starting…' : 'Start'}
                  </button>
                )}
                <a
                  href={uiURL}
                  target="_blank"
                  rel="noreferrer"
                  className="px-3.5 py-2 rounded-xl bg-surface-high text-text border border-outline-dim text-xs font-semibold no-underline hover:bg-surface-highest"
                >
                  Open UI ↗
                </a>
              </>
            )}
          </div>
        </div>

        <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-outline-dim/40 pt-4">
          <label className="text-[11px] text-text-dim">Port</label>
          <input
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ''))}
            className="w-20 bg-surface-high border border-outline-dim rounded-lg px-2 py-1 text-xs font-mono text-text"
          />
          <button
            onClick={() => act('port', () => saveLlamaswapSettings({ port: Number(port) }), 'Port saved; restart to apply.')}
            disabled={busy !== null || !port || Number(port) === state?.port}
            className="px-3 py-1.5 rounded-lg bg-surface-high border border-outline-dim text-[11px] font-semibold cursor-pointer disabled:opacity-50"
          >
            Save port
          </button>
          <button
            onClick={editing ? () => setEditing(false) : openConfig}
            disabled={busy !== null || !state?.config_exists}
            className="ml-auto px-3 py-1.5 rounded-lg bg-surface-high border border-outline-dim text-[11px] font-semibold cursor-pointer disabled:opacity-50"
          >
            {editing ? 'Close editor' : 'Edit YAML'}
          </button>
        </div>
      </section>

      {/* ─── One-click connect ─── */}
      <section className="bg-surface rounded-2xl p-5">
        <div className="flex items-baseline justify-between gap-3">
          <h2 className="font-semibold text-sm font-display m-0">Connect</h2>
          <span className="text-[11px] text-text-dim">
            {state?.running ? 'What a client needs' : 'The sidecar is stopped: these work once it runs'}
          </span>
        </div>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
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
          <div className="mt-4 border-t border-outline-dim/40 pt-3">
            <span className="text-[10px] uppercase tracking-[0.16em] text-text-dim font-label">
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
      </section>

      {/* ─── The YAML editor ─── */}
      {editing && (
        <section className="bg-surface rounded-2xl p-5">
          <div className="flex items-center justify-between gap-3">
            <h2 className="font-semibold text-sm font-display m-0">config.yaml</h2>
            <button
              onClick={saveConfig}
              disabled={busy !== null}
              className="btn-primary px-3.5 py-1.5 text-xs font-bold disabled:opacity-50"
            >
              {busy === 'config' ? '⟳ Saving…' : 'Save'}
            </button>
          </div>
          <div className="mt-3 h-[28rem] overflow-hidden rounded-xl border border-outline-dim">
            <YamlEditor value={yaml} onChange={setYaml} />
          </div>
        </section>
      )}

      {/* ─── Groups board ─── */}
      <section className="bg-surface rounded-2xl p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="font-semibold text-sm font-display m-0">Groups</h2>
            <p className="text-[11px] text-text-dim m-0 mt-1">
              Drag a model into a group: members of one swap group evict each other. Drop it on
              Ungrouped (or press ×) to take it out again.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Filter by name or id…"
              className="w-56 bg-surface-high border border-outline-dim rounded-lg px-2.5 py-1.5 text-[11px] text-text placeholder:text-text-dim"
            />
            <button
              onClick={addGroup}
              disabled={busy !== null}
              className="px-3 py-1.5 rounded-lg bg-surface-high border border-outline-dim text-[11px] font-semibold cursor-pointer disabled:opacity-50"
            >
              + Group
            </button>
          </div>
        </div>

        {(state?.recipes || []).length > 0 && (
          <div className="mt-4">
            <p className="text-[10px] font-bold uppercase tracking-wide text-text-muted m-0">
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
                    className="flex cursor-grab items-center gap-1.5 rounded-lg border border-outline-dim bg-surface-high/40 px-2 py-1"
                    title={r.slug}
                  >
                    <span className="text-[11px] text-text">{r.name}</span>
                    <span className="text-[10px] text-text-dim">
                      {r.memory_gb ? `${r.memory_gb} GB` : '—'}
                    </span>
                  </div>
                ))}
            </div>
          </div>
        )}

        <div className="mt-4 grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
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
      </section>

      {/* ─── Running models ─── */}
      <section className="bg-surface rounded-2xl p-5">
        <h2 className="font-semibold text-sm font-display m-0">Running now</h2>
        {(state?.running_models || []).length === 0 ? (
          <p className="text-[11px] text-text-dim m-0 mt-1">
            Nothing is up. A request through the sidecar starts the model it names.
          </p>
        ) : (
          <div className="mt-3 flex flex-col gap-2">
            {state.running_models.map((model) => {
              const measurement = measurements[model.model]
              return (
                <div key={model.model} className="rounded-xl border border-outline-dim bg-surface-high/40 px-3 py-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs font-semibold text-text">{model.name || model.model}</span>
                    <code className="text-[10px] text-text-dim font-mono">{model.model}</code>
                    <span className="text-[10px] text-text-dim">{model.state}</span>
                    <button
                      onClick={() => measure(model.model)}
                      disabled={Boolean(measuring[model.model])}
                      className="ml-auto px-2.5 py-1 rounded-lg bg-surface-high border border-outline-dim text-[10px] font-semibold cursor-pointer disabled:opacity-50"
                    >
                      {measuring[model.model] ? '⟳ Measuring…' : 'Measure'}
                    </button>
                  </div>
                  {measurement && (
                    <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-text-dim">
                      <span>
                        prefill <b className="text-text">{fmt(measurement.prefill_tps)}</b> tok/s
                        <span className="text-text-dim">
                          {' '}({measurement.prompt_tokens} tok / {measurement.ttft_ms} ms
                          {measurement.prefill_naive_tps ? `, naive ${fmt(measurement.prefill_naive_tps)}` : ''})
                        </span>
                      </span>
                      <span>
                        decode <b className="text-text">{fmt(measurement.decode_tps)}</b> tok/s
                        <span className="text-text-dim"> ({measurement.decode_tokens} tok / {measurement.total_ms} ms)</span>
                      </span>
                      <span>
                        memory <b className="text-text">{gb(measurement.memory_bytes)}</b>
                        {measurement.memory_source ? <span className="text-text-dim"> · {measurement.memory_source}</span> : null}
                      </span>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </section>
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

  return (
    <div
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault()
        if (dragging) onDropModel(dragging)
      }}
      className={`rounded-xl border p-3 min-h-[7rem] ${ungrouped ? 'border-dashed border-outline' : 'border-outline-dim bg-surface-high/30'}`}
    >
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-[11px] font-bold uppercase tracking-wide text-text-muted m-0">
          {title || name}
        </h3>
        <span className="text-[10px] text-text-dim">{members.length}</span>
      </div>
      <p className="text-[10px] text-text-dim m-0 mt-0.5">
        {usedGB > 0 ? `≈ ${usedGB.toFixed(0)} GB` : 'no memory data'}
        {totalGB ? ` of ${totalGB} GB` : ''}
        {freeGB > 0 ? ` · ${freeGB.toFixed(0)} GB free now` : ''}
        {unknown > 0 ? ` · ${unknown} unknown` : ''}
        {name && group?.swap === false ? ' · swap off' : ''}
        {name && group?.persistent ? ' · persistent' : ''}
      </p>
      {totalGB > 0 && (
        <div className="mt-1.5 h-1.5 w-full rounded-full bg-surface-highest overflow-hidden">
          <div
            className={`h-full rounded-full ${overFree ? 'bg-error' : fill > 70 ? 'bg-tertiary' : 'bg-primary'}`}
            style={{ width: `${fill}%` }}
          />
        </div>
      )}
      <div className="mt-2 flex flex-col gap-1.5">
        {visible.map((id) => (
          <div
            key={id}
            draggable
            onDragStart={() => onDragStart(id)}
            onDragEnd={() => onDragStart(null)}
            className="group cursor-grab rounded-lg border border-outline-dim bg-surface px-2 py-1.5"
          >
            <div className="flex items-center gap-1.5">
              {running.has(id) && <span className="inline-flex h-1.5 w-1.5 rounded-full bg-primary" />}
              <span className="text-[11px] text-text truncate">{models[id]?.name || id}</span>
              <div className="ml-auto flex items-center gap-1 opacity-60 transition-opacity group-hover:opacity-100">
                {!ungrouped && (
                  <button
                    onClick={(e) => { e.stopPropagation(); onRemove(id) }}
                    title="Take out of the group"
                    className="cursor-pointer border-none bg-transparent p-0 text-[10px] leading-none text-text-dim hover:text-error"
                  >
                    ×
                  </button>
                )}
              </div>
            </div>
            <div className="flex items-center gap-2 text-[10px] text-text-dim">
              <code className="font-mono">{id}</code>
              {measurements[id]?.decode_tps ? <span>{fmt(measurements[id].decode_tps)} tok/s</span> : null}
              {measurements[id]?.memory_bytes
                ? <span>{gb(measurements[id].memory_bytes)}</span>
                : recipes?.get(id)?.memory_gb
                  ? <span>{recipes.get(id).memory_gb} GB</span>
                  : null}
            </div>
          </div>
        ))}
        {visible.length === 0 && (
          <p className="text-[10px] text-text-dim m-0 py-2">
            {members.length === 0 ? 'Nothing here.' : `All ${members.length} filtered out by the search.`}
          </p>
        )}
      </div>
    </div>
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

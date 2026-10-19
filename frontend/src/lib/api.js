import { useAuth } from '../auth'

/* ─── Registries and local recipes ─────────────────────────────────────────
 *
 * Where recipes come from, and the copies the user owns. All of it is
 * admin-only on the daemon side, and unlike the catalog these calls are not
 * polled: they run on a button press and their outcome is the answer.
 *
 * So instead of throwing, every helper resolves to
 *   { ok: true,  data }                 on a 2xx
 *   { ok: false, status, error }        otherwise
 * with `error` already a sentence a human can read: the daemon's own `detail`
 * when it sent one (a taken slug, an app still installed), our own wording for
 * the two cases where the answer is about the person, not the request.
 */

const NOT_ADMIN = 'Administrator rights required.'

async function request(method, url, body) {
  let res
  try {
    res = await fetch(url, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    // The daemon went away mid-click (restart, tunnel dropped). Say so rather
    // than leaving the form's spinner running forever.
    return { ok: false, status: 0, error: 'Could not reach the Hub.' }
  }

  let data = null
  try {
    data = await res.json()
  } catch {
    // 204s and git's stderr-only failures have no JSON body.
  }

  if (res.ok) return { ok: true, status: res.status, data }
  if (res.status === 403) return { ok: false, status: 403, error: NOT_ADMIN }

  const detail = data?.detail || data?.message || data?.error
  return {
    ok: false,
    status: res.status,
    error: typeof detail === 'string' && detail.trim() ? detail : `Request failed (${res.status}).`,
  }
}

/* Sources */

// { sources: [{ id, url, ref, subpath, commit, synced_at, error, recipes }],
//   local_count, syncing }
export function fetchRegistries() {
  return request('GET', '/api/registries')
}

// `urls` is the textarea's text: commas or newlines, the daemon splits it.
// The answer is the whole list plus the lines it could not parse.
export function addRegistries(urls) {
  return request('POST', '/api/registries', { urls })
}

export function syncRegistries() {
  return request('POST', '/api/registries/sync')
}

export function syncRegistry(id) {
  return request('POST', `/api/registries/${encodeURIComponent(id)}/sync`)
}

export function removeRegistry(id) {
  return request('DELETE', `/api/registries/${encodeURIComponent(id)}`)
}

/* Engines */

// A model runtime installed once on the machine, not in a container per app.
// { engines: [{ name, installed, version, latest, path }] }
export function getEngines() {
  return request('GET', '/api/engines')
}

// 409 when the same version is already in place; the daemon's sentence says so.
export function installEngine(name) {
  return request('POST', `/api/engines/${encodeURIComponent(name)}/install`)
}

// Installs the latest release; 409 while a native recipe using it runs.
export function updateEngine(name) {
  return request('POST', `/api/engines/${encodeURIComponent(name)}/update`)
}

// Removes the whole engine directory; 409 while a native recipe using it runs.
export function deleteEngine(name) {
  return request('DELETE', `/api/engines/${encodeURIComponent(name)}`)
}

// Engine-wide serve flags: { cortheon, dashboard }. Every native recipe on the
// engine gets them at launch.
export function getEngineSettings(name) {
  return request('GET', `/api/engines/${encodeURIComponent(name)}/settings`)
}

export function saveEngineSettings(name, settings) {
  return request('PUT', `/api/engines/${encodeURIComponent(name)}/settings`, settings)
}

/* Benchmarks */

// One real request against a running recipe: prefill, decode and the memory it
// holds. The daemon records the result, so the card keeps showing it.
export function benchmarkRecipe(slug) {
  return request('POST', `/api/recipes/${encodeURIComponent(slug)}/benchmark`)
}

// { benchmarks: { slug: measurement } }
export function getBenchmarks() {
  return request('GET', '/api/recipes/benchmarks')
}

/* llama-swap sidecar */

// The sidecar the Hub installs and runs itself: the upstream binary, its YAML
// config and live state. Admin only, like the engines.
export function getLlamaswap() {
  return request('GET', '/api/llamaswap')
}

export function installLlamaswap(version = '') {
  const suffix = version ? `?version=${encodeURIComponent(version)}` : ''
  return request('POST', `/api/llamaswap/install${suffix}`)
}

export function updateLlamaswap() {
  return request('POST', '/api/llamaswap/update')
}

export function startLlamaswap() {
  return request('POST', '/api/llamaswap/start')
}

export function stopLlamaswap() {
  return request('POST', '/api/llamaswap/stop')
}

export function saveLlamaswapSettings(settings) {
  return request('PUT', '/api/llamaswap/settings', settings)
}

// { yaml: "..." }
export function getLlamaswapConfig() {
  return request('GET', '/api/llamaswap/config')
}

export function saveLlamaswapConfig(yaml) {
  return request('PUT', '/api/llamaswap/config', { yaml })
}

// Rewrites only the groups section, keeping the rest of the file as it was.
export function saveLlamaswapGroups(groups) {
  return request('PUT', '/api/llamaswap/groups', groups)
}

// One real request through the sidecar, reported as prefill/decode tok/s plus
// the memory the model holds afterwards.
export function measureLlamaswapModel(model) {
  return request('POST', `/api/llamaswap/models/${encodeURIComponent(model)}/measure`)
}

/* Local recipes */

// { recipes: [{ slug, name, origin, modified }] }
export function fetchLocalRecipes() {
  return request('GET', '/api/local-recipes')
}

// Copy a recipe into local/<newSlug>; 409 when the slug is taken.
export function forkRecipe(slug, newSlug) {
  return request('POST', `/api/recipes/${encodeURIComponent(slug)}/fork`, { slug: newSlug })
}

// 409 while the recipe is installed or running; the directory is kept then.
export function deleteLocalRecipe(slug) {
  return request('DELETE', `/api/recipes/${encodeURIComponent(slug)}/local`)
}

// One global place to notice that our session stopped being valid.
//
// The Hub polls /api/recipes every few seconds from App, plus metrics over a
// WebSocket, plus whatever page is open — so rather than teach each of those
// call sites about 401s, wrap fetch once. When an admin revokes an account or
// a session expires, the very next poll bounces the user to the sign-in
// screen instead of leaving them staring at a frozen, empty catalog.
export function installAuthInterceptor() {
  const original = window.fetch.bind(window)

  window.fetch = async (input, init) => {
    const res = await original(input, init)
    if (res.status !== 401) return res

    const url = typeof input === 'string' ? input : input?.url || ''
    // Only our own API speaks for our session; a 401 from anything else
    // (an upstream app's endpoint, say) says nothing about the Hub login.
    const isHubApi = url.startsWith('/api/') || url.includes(`${window.location.origin}/api/`)
    // The sign-in routes 401 on bad credentials — that is the form's error to
    // show, not a reason to tear down a session that never existed.
    const isAuthRoute = /\/api\/auth\/(login|register|state)$/.test(url)
    if (isHubApi && !isAuthRoute) useAuth.getState().clearSession()
    return res
  }
}

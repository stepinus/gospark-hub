/* Where this Spark can be reached: the picker the Agents page and the
 * llama-swap page both use. The daemon does the looking (GET /api/system/connect
 * asks mDNS, Tailscale and the routing table), so the browser and the server
 * agree on the list; these helpers only turn it into URLs.
 */

export const KIND_LABEL = {
  // "origin" is the address this page was actually loaded from. The daemon
  // only lists it when it is not one of the names the box can see on itself
  // -- i.e. when a tunnel or reverse proxy is in front.
  origin: 'This address',
  mdns: 'mDNS name',
  tailscale: 'Tailscale',
  ip: 'LAN IP',
}

// The order the picker shows them in: reachable from anywhere first, then the
// LAN, then the addresses that only work on this machine's own subnet.
const KIND_ORDER = { origin: 0, tailscale: 1, ip: 2, mdns: 3 }

/* hostOptions turns connect-info candidates into `{ host, label, note }`, one
 * per address, for a caller that builds its own URLs (llama-swap's port is not
 * the Hub's). Loopback never appears: a page opened on 127.0.0.1 would
 * otherwise hand out an address that works nowhere else.
 */
const isLoopback = (host) => host === 'localhost' || host === '::1' || /^127\./.test(host)

export function hostOptions(info) {
  const seen = new Set()
  const out = []
  const originHost = info?.origin ? new URL(info.origin).hostname : ''
  for (const c of info?.candidates || []) {
    if (!c.host || seen.has(c.host) || isLoopback(c.host)) continue
    seen.add(c.host)
    out.push({ host: c.host, label: labelFor(c), note: c.note, kind: c.kind })
  }
  if (originHost && !isLoopback(originHost) && !out.some((o) => o.host === originHost) && info?.external_origin) {
    out.unshift({ host: originHost, label: KIND_LABEL.origin, note: 'The address you are using right now.', kind: 'origin' })
  }
  return out.sort((a, b) => (KIND_ORDER[a.kind] ?? 9) - (KIND_ORDER[b.kind] ?? 9))
}

// Two tailscale candidates are one kind: name the number apart from the name,
// or the picker shows the same word three times.
function labelFor(c) {
  if (c.kind !== 'tailscale') return KIND_LABEL[c.kind] || c.kind
  if (c.host.includes(':')) return 'Tailscale IPv6'
  if (/^100\./.test(c.host)) return 'Tailscale IP'
  return 'Tailscale name'
}

export default function AddressPicker({ addresses, address, onPick }) {
  if (addresses.length < 2) return null
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <span className="text-xs text-text-dim">Address:</span>
      <div className="flex flex-wrap gap-1 rounded-xl border border-outline-dim bg-surface-high p-0.5">
        {addresses.map((a) => (
          <button
            key={a.url}
            type="button"
            onClick={() => onPick(a.url)}
            title={a.note}
            className={`cursor-pointer rounded-lg px-2.5 py-1 text-[11px] font-semibold transition-all ${
              address.url === a.url ? 'bg-primary text-primary-on' : 'bg-transparent text-text-muted hover:text-text'
            }`}
          >
            {a.label}
          </button>
        ))}
      </div>
    </div>
  )
}

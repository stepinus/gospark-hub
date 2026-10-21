# gospark-hub

[![release](https://img.shields.io/github/v/release/stepinus/gospark-hub?label=linux%2Farm64)](https://github.com/stepinus/gospark-hub/releases/latest)

A **Go rewrite of [spark-ai-hub](https://github.com/WaxacaBytes/spark-ai-hub)** — the
one-click AI app launcher and model server console for the NVIDIA DGX Spark.

The Python original (FastAPI daemon + React frontend + Caddy front door) works, but it
drags a Python runtime, a virtualenv and a reverse-proxy container behind every install.
This rewrite keeps the same product — same pages, same API shape, same recipe format —
and ships it as **one static binary with the UI embedded**, so a Spark can be set up by
copying a file and starting a unit instead of bootstrapping a Python stack.

The frontend was rebuilt on [shadcn/ui](https://ui.shadcn.com) on top of the original
layout, and is compiled into the daemon (`go:embed`) — a release binary needs no
`frontend/` directory beside it.

---

## What is new compared to upstream

### llama-swap, managed from the UI

Upstream leaves llama-swap to the user. Here the Hub owns it end to end:

- installs, updates, starts and stops the sidecar itself (`llama-swap` release tarball,
  checksum-verified, running as a `systemd --user` unit);
- edits `config.yaml` in place through a YAML AST, so comments, `apiKeys` and every
  model definition survive a change;
- **groups board with drag and drop** — drag a model into a group, drag it out again,
  create groups, search by name or id; the memory bar under each group subtracts what is
  already used on the box and turns red when the group cannot fit;
- per-model **Measure** button: runs a real streaming benchmark and shows prefill slope,
  decode rate and the memory the model's own process holds.

### Recipe work

- **Registry sources** are git repositories, vendored at a pinned commit; sync is explicit
  and per-source, with a diff of what a source carries.
- **Local recipes** live beside the vendored ones and are never touched by a sync;
  "Create copy" promotes a catalogue recipe into an editable one.
- Each recipe's `docker-compose.yml` is editable from its page, with a "restore default"
  that goes back to the registry copy.
- Benchmarks are stored per recipe (`data/benchmarks.json`) instead of being a one-off
  terminal run: the Running page shows the last measurement of everything that is up.
- Cards can be hidden from the Store and Agents pages (client-side preference) without
  stopping anything.

### Native recipes — a model server without a container

A recipe can declare a **native** launch: the Hub installs the inference engine once
under `~/.config/gospark/engines/<engine>/<version>`, and the recipe becomes just a
checkpoint, a flag list and a port. Launching it runs
`gospark-native-<slug>` as a `systemd --user` unit — no image, no container, nothing in
`docker ps`. Engines can be installed, updated and deleted from the Engines page, and
engine-wide serve flags (`--cortheon`, `--dashboard`, …) are persisted per engine and
filtered against the binary's own `serve --help` before they are passed.

A model the installed engine cannot serve at all can still ship: a recipe may carry an
`engine_build` that extracts a patched client out of an image into the engine home, and
the Hub runs that build instead of the stock one.

### Everything else

- **Auth**: first-run administrator, request-an-account flow with admin approval, per-user
  API keys for `/v1` and `/mcp`, session cookies with a plain sha256 token store.
- **Agents page**: `/v1` (OpenAI-compatible) and `/mcp` are served by the Hub and follow
  whichever model is up; the page prints ready-to-paste setup for Claude Code, Codex,
  Cline, OpenCode, Hermes and friends, plus a `sah` CLI one-liner.
- **Connect addresses** are collected by the daemon (mDNS name, Tailscale name, Tailscale
  IPv4/IPv6, LAN IP, tunnel origin) instead of being guessed from `window.location`.
- **Files**: a per-user media library for images, video, 3D models and audio produced by
  the apps, with uploads, previews, download and link copying.
- **Benchmarks** measure both prefill and decode, and separate the two: prompt tokens over
  a single TTFT is not prefill, so the number shown is a slope over two requests.

---

## Layout

```
cmd/sahd/            the daemon: wiring, startup, graceful shutdown
internal/
  api/               HTTP routes, one package per area (auth, recipes, engines,
                     llamaswap, mcp, models, files, registries, system, oauth)
  auth/              sessions, users, API keys
  registry/          recipe parsing, git sources, vendoring, local copies
  native/            systemd --user launches for native recipes
  engines/           engine install/update/build extraction
  llamaswap/         sidecar binary management and config.yaml editing
  proxy/             optional Caddy front door
  modelproxy/        /v1 and /mcp fronting of whatever is served
  bench/             the benchmark arithmetic
  media/             the files library
  monitor/           metrics for the UI
  web/               router, static assets, SPA fallback
frontend/            React + Vite + shadcn/ui, embedded via go:embed
sah/                 the `sah` launcher CLI served to clients
registry/recipes/    recipes authored here
docs/                parity, acceptance and UI notes
```

## Build

Requirements: Go 1.27+, Node 24.

```sh
cd frontend && npm ci && npm run build      # produces frontend/dist
cd .. && go build -o gospark-sahd ./cmd/sahd
```

The frontend must be built **before** the daemon: the UI is embedded into the binary.
A release binary served without `frontend/` next to it is the normal case.

Cross-compiled release binaries for `linux/arm64` are built by
[the release workflow](.github/workflows/release-linux-arm64.yml) on every `v*` tag:

```sh
gh release download v0.1.0 -R stepinus/gospark-hub
sha256sum -c gospark-sahd.sha256
chmod +x gospark-sahd && ./gospark-sahd
```

## Run

```sh
PORT=9110 PUBLIC_PORT=9100 PROXY_ENABLED=false ./gospark-sahd
```

`PROXY_ENABLED=false` makes the daemon serve the public port itself — no Caddy
container. With it left on (the default) a Caddy front door is used for the app routes
under `/app/<slug>/`.

| variable | default | what it does |
| --- | --- | --- |
| `HOST` | `0.0.0.0` | bind address |
| `PORT` | `9110` | daemon port (behind the front door) |
| `PUBLIC_PORT` | `9100` | the port clients see |
| `PROXY_ENABLED` | `true` | run the Caddy front door |
| `AUTH_ENABLED` | `true` | session auth; off means anyone on the network |
| `SESSION_TTL_DAYS` | `30` | session lifetime |
| `REGISTRY_HOME` | `~/.config/gospark/registry` | sources, vendored clones, local recipes |
| `ENGINE_HOME` | `~/.config/gospark/engines` | installed inference engines |
| `DATA_DIR` | `./data` | database, logs, benchmarks |
| `DB_PATH` | `<DATA_DIR>/spark-ai-hub.db` | sqlite file |
| `LLAMASWAP_HOME` | `~/.config/gospark/llamaswap` | sidecar binary and config |

Each variable is also accepted with the upstream prefix (`SPARK_AI_HUB_PORT`, …), so an
existing deployment's environment keeps working.

### Container

```sh
docker build -t gospark-hub .
docker run -d --name gospark-hub -p 9106:9106 \
  -v gospark-data:/data gospark-hub
```

The image builds the UI, embeds it into the binary and bakes the recipe catalogue at
`/app/registry/recipes`. The volume at `/data` holds the database, agent files, logs,
vendored recipe clones and downloaded engines, so it is the only thing to keep. Defaults
inside the image: `PORT=PUBLIC_PORT=9106`, `PROXY_ENABLED=false` (the daemon serves the
port itself, no Caddy sidecar), `DATA_DIR=/data`.

Container and engine recipes are started with the `docker` CLI, which the image does not
carry: such a deployment needs the CLI and the socket mounted in
(`-v /usr/bin/docker:/usr/bin/docker:ro -v /var/run/docker.sock:/var/run/docker.sock`),
or should stay with native recipes and the llama-swap sidecar over the network.

## Development

```sh
./dev-daemon.sh                             # rebuild + restart the daemon on Go changes
cd frontend && HUB_API=http://127.0.0.1:9110 npm run dev   # Vite with API/ws proxying
go test ./...                               # the gate
```

`docs/ACCEPTANCE.md` holds the parity checklist against the upstream daemon,
`docs/PARITY.md` describes the side-by-side run that compares both APIs, and
`docs/ui-shadcn.md` is the design contract for the frontend.

## Credits

Original project: [WaxacaBytes/spark-ai-hub](https://github.com/WaxacaBytes/spark-ai-hub) —
the recipe catalogue, the product thinking and the UI this rewrite follows all come from
there. This repository is a port, not a fork of its history.

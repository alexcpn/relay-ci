# Relay CI — Web UI

Angular 17 SPA that talks to the master's REST endpoints (`/api/v1/builds`,
`/api/v1/builds/{id}`, `/api/v1/workers`).

## Develop

```bash
# in one terminal: master (serves /api on :8080 by default)
go run ./cmd/master

# in another: dev server with hot reload
make web-install   # one-time
make web-dev       # http://localhost:4200
```

`proxy.conf.json` forwards `/api/*` from the dev server to `http://localhost:8080`
so there's no CORS to fight. Override the target if your master runs elsewhere.

## Build

```bash
make web   # produces web/dist/
```

The bundle is a plain static SPA; serve it from any HTTP server or behind the
master once the embed step is wired up (see backlog P8).

## Routes

- `/builds` — list of builds with state, repo, branch, commit, tasks summary
- `/builds/:id` — build detail with per-stage state, duration, exit code, deps

Both views poll the REST API (3s for the list, 2s for detail). Real-time
streaming via SSE/WebSocket is a separate backlog item.

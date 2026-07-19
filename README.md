# Arcway Backend

Arcway Backend is the control plane for a shared Xray node service. It provides the HTTP API and embedded web console used to manage servers, nodes, users, packages, subscriptions, traffic limits, certificates, speed tests, and user-scoped server grants.

## Repository Contract

The Go server embeds the production web console from `internal/web/dist/` at compile time. This repository intentionally versions that directory so every commit can be tested and built without checking out the separate frontend repository.

For each frontend release:

1. Build `violetaini/arcway-frontend` with `npm ci --include=dev && npm run build`.
2. Replace this repository's `internal/web/dist/` with the generated `dist/` directory.
3. Review and commit the changed `index.html` and hashed assets together with the backend release.

Do not hand-edit the embedded bundle.

## Build and Test

Go 1.26 is required.

```bash
go mod verify
go test ./...
go build -trimpath -o arcway ./cmd/server
```

The release helper builds Linux and Windows binaries from the committed frontend snapshot:

```bash
./build.sh
```

## Run

The server listens on port `12889` by default and stores runtime data outside the source tree.

```bash
PORT=12889 DATABASE_PATH=./data/arcway.db go run ./cmd/server
```

Docker Compose uses `ghcr.io/violetaini/arcway-backend:latest`:

```bash
docker compose up -d
```

Release installation:

```bash
curl -fsSL https://raw.githubusercontent.com/violetaini/arcway-backend/main/install.sh | sudo bash
```

Installation and online updates use the public GitHub Release assets and verify their published SHA-256 digests before replacing binaries.

## Deployment

- `deploy/arcway.service` contains a systemd service template.
- `deploy/arcway.nginx.conf` contains the reverse-proxy template.
- `docker-compose.yml` runs the published GHCR image in host-network mode.
- `docs/BASELINE.md` records the upstream baseline used for this fork.

Never commit runtime databases, environment files, private keys, access tokens, logs, or generated debug binaries.

## License and Attribution

This project is derived from `violetaini/miaomiaowuX` and the original `iluobei/miaomiaowuX` work. The upstream project is distributed under the MIT License; its copyright and permission notice are retained in [LICENSE](LICENSE).

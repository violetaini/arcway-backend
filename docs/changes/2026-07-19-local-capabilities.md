# Local capabilities migration

Date: 2026-07-19

## Scope

The backend now uses a compile-time local capability policy. It no longer
contacts an activation service or reads an activation key, machine fingerprint,
signed response, feature token, public key, persisted activation status, or
heartbeat configuration.

All shipped capabilities are enabled:

- embedded Xray
- limiter
- server sharing and federation
- local and remote speed testing

Commercial server, managed-node, and user-count quotas were removed. User
authentication, administrator RBAC, package traffic limits, speed limits,
device limits, and per-user permissions remain unchanged.

## Compatibility

The master continues to send Agent capability data with the existing
`license_status` WebSocket message name so released Agents keep working. Its
payload is always valid, advertises every local capability, and uses zero maxima
to represent unlimited resources. No activation data is included.

The following panel-only activation endpoints were removed:

- `/api/admin/license/status`
- `/api/admin/license/usage`
- `/api/admin/license/settings`
- `/api/user/license/status`

Existing `license_key`, `license_server_url`, and `license_status` database rows
are ignored and intentionally left untouched to avoid destructive migration of
an existing installation.

## Verification

Focused tests cover the fixed local feature set, rejection of unknown feature
names, the legacy Agent payload shape, and unlimited resource maxima.

## Build and release chain

The independent frontend now lives in `frontend/` and builds directly to
`internal/web/dist`. Docker, local build scripts, and GitHub Actions use the
same source tree and lockfile with Node.js 22 and Go 1.26.

The release chain no longer checks out a private frontend repository, requires
`FRONTEND_PAT`, accepts `LICENSE_PUB_KEY`, or injects authorization material
with Go linker flags. Frontend package metadata is synchronized from
`internal/version/version.go` during a release, and the frontend and backend
are committed and tagged together.

Build-chain verification completed with:

- frontend type checking, unit tests, and production build
- clean `npm ci` and frontend build in the Node.js 22 Docker stage
- a complete Linux ARM64 multi-stage image build with CGO enabled
- a Linux AMD64 cross-build with Go 1.26 and CGO disabled
- a container smoke test returning HTTP 200 for the embedded frontend
- shell syntax, YAML parsing, and whitespace/error checks

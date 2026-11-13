# resources-local-development-only

This directory holds everything that is **only used for local development** (`make run`, `make dev`).
It is excluded from the Docker build via `.dockerignore`.

## Contents

**In the repository** — documentation and helper notes, no credentials:

| File | Purpose |
|---|---|
| `Safety Automation product specification.txt` | Product specification: purpose, features, architecture, data and security model |
| `development_scripts.txt` | SQL snippets used during development (reset schema, insert test data) |
| `commands-used-during-development.txt` | Build and redeploy commands for a development cluster |
| `go-service-mesh-readme-txt` | How to enable Istio sidecar injection for a namespace |

**Local only — you create these yourself; they are never committed** (`.gitignore` excludes them):

| File / Directory | Purpose |
|---|---|
| `.env` | Environment variables for local development (Oracle DSN, session secret, `TNS_ADMIN`, …). Start from `../.env.example`. |
| `Wallet_devdb/` | Oracle Cloud wallet files (`cwallet.sso`, `tnsnames.ora`, `sqlnet.ora`, …) for your development database |

## Why this directory is excluded from the Docker build

The `.dockerignore` file at the project root lists `resources-local-development-only/`, which
tells Docker to exclude this entire directory from the build context. This means:

- `docker build` never sees these files — they cannot accidentally end up in the image.
- The Dockerfile does **not** `COPY` the wallet or `.env` from here. Configuration reaches
  the running container via Kubernetes Secrets and ConfigMaps, managed by the Helm chart.
- This separation is required for Red Hat Marketplace certification: certified images must not
  contain credentials or environment-specific configuration baked in.

## How local development uses these files

The `Makefile` sources `.env` before starting the application:

```bash
make run   # sources .env, then go run ./cmd/server/main.go
make dev   # sources .env, then starts Air hot-reload
```

`TNS_ADMIN` in `.env` points at `./resources-local-development-only/Wallet_devdb`, which is
where the Oracle native client loads the wallet from. This mirrors what the Helm chart does
in production — the only difference is that in production the wallet files come from a
Kubernetes Secret mounted at `/opt/oracle/instantclient_23_26/network/admin`.

## What goes in production instead

| Local (this directory) | Production (Helm chart) |
|---|---|
| `.env` | Kubernetes ConfigMap (non-sensitive) + Secret (sensitive) |
| `Wallet_devdb/` | Kubernetes Secret mounted as a volume at `TNS_ADMIN` path |

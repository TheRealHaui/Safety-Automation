# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**Safety Automation** — an OpenShift Resilience Testing Platform. A Go monolith that executes chaos/resilience scenario tests (pod kill and start, network latency, resource stress) against OpenShift Deployments and DeploymentConfigs, with live log streaming via SSE and PDF audit report generation.

## Commands

```bash
make run      # Run app (sources .env for TNS_ADMIN + Oracle vars first)
make dev      # Run with hot-reload via Air
make build    # Build binary to bin/resilience-tester
make test     # go test ./...
make vet      # go vet ./...
make tidy     # go mod tidy
make clean    # Remove bin/
```

Hot-reload requires Air: `go install github.com/air-verse/air@latest`

## Architecture

Single Go binary — Chi v5 router, server-side HTML + htmx partials, Tailwind CSS CDN, Oracle DB (godror; supports EZConnect, TNS, and mTLS wallet), client-go for OpenShift API, goroutine worker pool for test execution.

**Key internal packages:**
- `internal/scenarioImplementation/` — `TestScenario` interface + implementations. New scenarios must implement both `Run()` and `CreateFinalVerdicts()` and be registered in the `Registry` map in `TestScenario.go` and seeded in `main.go`
- `internal/testRunExecution/` — Worker pool and test run lifecycle management
- `internal/logger/` — Run-scoped `DatabaseSSELogger` that writes to both Oracle DB and SSE stream simultaneously
- `internal/backend/restClient/` — client-go wrapper for listing Deployments/DeploymentConfigs and launching K6 jobs
- `internal/backend/database/` — Oracle repository layer
- `internal/backend/grafana/` — Grafana HTTP client, `DashboardUID` constant, `OrgID` var (auto-discovered at startup)
- `internal/backend/prometheus/` — Prometheus query helpers (scrape interval, metric queries)
- `internal/httpHandler/` — HTTP handlers (all share `SharedHTTPRequestDependenciesHandlerStruct`)
- `internal/applicationModelStructs/` — Shared model types (Workload, status enums, etc.)
- `internal/config/` — Environment variable loading
- `web/templates/` — HTML templates embedded in the binary at build time

## Environment

Copy `.env.example` to `.env`. Critical vars:

| Variable | Notes |
|---|---|
| `ORACLE_DSN` | Mode 1 (EZConnect): `user/pass@//host:1521/service` — Mode 2/3 (TNS): `user/pass@alias` |
| `TNS_ADMIN` | Path to wallet/TNS directory — **must be set before process start**, not at Go runtime. Mode 1 (EZConnect) does not need this. |
| `KUBECONFIG_PATH` | Optional; auto-detects active context otherwise |
| `SESSION_SECRET` | Change in production |

The Makefile sources `.env` before running because Oracle's native client (not Go) reads `TNS_ADMIN` at library load time.

## OpenShift Deployment

RBAC resources (Role, RoleBinding, ServiceAccount) are bundled in the Helm chart under `chart/templates/` and applied automatically by `helm install`. 
Some additional manual copies for local dev (non-Helm) are in `resources-local-development-only/yaml/rbac/` too.

Build and deploy on OpenShift:
```bash
oc project automation-safety-shopping-demo
oc start-build hoplon && oc logs -f bc/hoplon
```

The Dockerfile uses a multi-stage build and embeds the Oracle Instant Client and wallet (from `resources-embedded/`) into the image. The runtime user is UID 1001 for OpenShift security constraints.

## Adding a Scenario

1. Create a struct in `internal/scenarioImplementation/` implementing the `TestScenario` interface from `TestScenario.go`:
   ```go
   type TestScenario interface {
     Run(
       ctx context.Context,
       testRunId int64,
       target applicationModelStructs.Workload,
       urls []database.TestRunURL,
       kubernetesClient *restClient.KubernetesClient,
       db *sql.DB,
       databaseSSELogger *logger.DatabaseSSELogger,
     ) error

     CreateFinalVerdicts(
       ctx context.Context,
       testRunId int64,
       target applicationModelStructs.Workload,
       db *sql.DB,
       sseLogger *logger.DatabaseSSELogger,
     ) error
   }
   ```
2. Register it in the `Registry` map in `internal/scenarioImplementation/TestScenario.go`
3. Add a seed entry in `main.go` (scenario type + any finding types it uses)

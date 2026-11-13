# Helm Chart Deployment

This document explains how Safety Automation is deployed to OpenShift using the Helm chart in the `chart/` directory.

## Prerequisites

- Helm 3.x installed locally
- `oc` CLI logged in to your OpenShift cluster
- The Safety Automation container image published to a registry the cluster can pull from
- The Oracle wallet files for your target database

## Chart structure

```
chart/
  Chart.yaml                  # chart metadata, version, RH Marketplace annotations
  values.yaml                 # default values — all tunable parameters
  .helmignore
  templates/
    _helpers.tpl              # shared template helpers (name, labels, etc.)
    configmap.yaml            # non-sensitive env vars (APP_PORT, K6_IMAGE, ...)
    secret-credentials.yaml   # sensitive env vars (ORACLE_DSN, SESSION_SECRET, ...)
    secret-wallet.yaml        # Oracle wallet files as a mounted Secret
    deployment.yaml           # main application Deployment
    service.yaml              # ClusterIP Service
    route.yaml                # OpenShift Route (TLS edge termination)
    serviceaccount.yaml       # ServiceAccount for the pod
    role.yaml                 # RBAC Role (list Deployments, create K6 Jobs, manage VirtualServices)
    rolebinding.yaml          # binds Role to ServiceAccount
    NOTES.txt                 # printed after helm install / helm upgrade
```

## How configuration reaches the running pod

```
values.yaml  ──►  configmap.yaml     ──►  Kubernetes ConfigMap  ──►  envFrom  ──►  os.Getenv() in Go
             ──►  secret-credentials ──►  Kubernetes Secret     ──►  envFrom  ──►  os.Getenv() in Go
             ──►  secret-wallet      ──►  Kubernetes Secret     ──►  volumeMount at TNS_ADMIN path
```

The Go application reads all configuration exclusively via `os.Getenv()` — no file parsing, no
library needed. Kubernetes sets the environment variables before the process starts, just like
the Makefile sources `.env` for local development.

## Automatic rolling restart on config changes

Kubernetes does **not** restart pods when a ConfigMap or Secret is updated in place. Without
intervention, `helm upgrade` would update the ConfigMap/Secret but leave the running pod with
the old values until it is manually restarted.

The Deployment template solves this with checksum annotations:

```yaml
annotations:
  checksum/config:               {{ sha256sum of configmap.yaml content }}
  checksum/secret-credentials:   {{ sha256sum of secret-credentials.yaml content }}
  checksum/secret-wallet:        {{ sha256sum of secret-wallet.yaml content }}
```

Helm recomputes these hashes on every `helm upgrade`. When any config content changes, the
hash changes, the pod spec changes, and Kubernetes triggers a rolling restart automatically.

## Local development workflow (three loops)

| Loop | Command | Config source |
|---|---|---|
| Inner (Go process) | `make run` / `make dev` | `resources-local-development-only/.env` + wallet directory |
| Container | `docker build && docker run --env-file ... -v wallet:/opt/oracle/...` | bind mount |
| Cluster | `helm upgrade --install hoplon ./chart -f resources-local-development-only/values.dev.yaml` | Kubernetes ConfigMap + Secrets |

The `resources-local-development-only/` directory is excluded from the Docker build via
`.dockerignore` and is **never** baked into the image. See the README in that directory.

The dev values file with real credentials lives at
`resources-local-development-only/values.dev.yaml` — it is checked into git alongside the
wallet files and `.env`, all under the same "excluded from Docker, visible in git" convention.

## Chart vs. Docker image — two separate artifacts

The Helm chart archive (`hoplon-0.1.1.tgz`) contains **only** Kubernetes templates and
configuration. It does not bundle the application binary or any Docker image.

The Docker image is a completely independent artifact that must be:
1. Built from the Dockerfile in this repository by DynaCov.
2. Pushed to a registry that the customer's cluster can pull from.
3. Referenced in the chart via `image.repository` + `image.tag`.

At pod start time, Kubernetes fetches the image directly from that registry — the chart
just tells it where to look.

**Current distribution registry:** Google Artifact Registry (private, access via GCP Service Account)
```
# Helm chart
oci://europe-west6-docker.pkg.dev/automation-safety-485518/dynacov-safety-automation-public/dynacov-safety-automation

# Container image
europe-west6-docker.pkg.dev/automation-safety-485518/dynacov-safety-automation/container
```
This default is set in `chart/values.yaml`. Because the registry is private, customers
must create an image pull secret from a GCP Service Account JSON key before installing:

```bash
oc create secret docker-registry gar-pull-secret \
  --docker-server=europe-west6-docker.pkg.dev \
  --docker-username=_json_key \
  --docker-password="$(cat sa-key.json)" \
  -n <your-namespace>
```

Then reference it at install time:
```bash
helm install hoplon ... --set image.pullSecrets[0].name=gar-pull-secret
```

DynaCov provides the Service Account JSON key to customers separately.

**Future:** once the image is certified on Red Hat Partner Connect it will move to
`registry.connect.redhat.com/dynacov/hoplon` — no pull secret required for that registry.

## Installing the chart

There are two ways to install: from the **local chart directory** (development) or from
**Google Artifact Registry** (the published release). The difference matters for more than
just convenience — it also affects what version information Safety Automation displays in the web UI
footer and the startup log. See the comparison table below.

| | Local chart (`./chart`) | From GAR |
|---|---|---|
| Chart source | Local `chart/` directory | OCI pull from GAR on install |
| Container image | Typically an OpenShift internal registry image built via `oc start-build` | `container` from GAR, built with `docker build --build-arg GIT_COMMIT=...` |
| Git commit in version | **Not shown** — `oc start-build` does not pass `GIT_COMMIT` build arg | **Shown** — e.g. `v0.1.1 (a3f2c1d)` |
| Typical use | Day-to-day development iterations | Testing the published release; customer-facing installs |

The git commit appears in the version string only when the container image was built with the
`--build-arg GIT_COMMIT=$(git rev-parse --short HEAD)` flag (see `Dockerfile`). Images built
via `oc start-build` skip that arg, so the footer shows only `v0.1.1` without a commit hash.

---

### Option A — Local chart directory (development)

Use this when iterating locally. The chart is read directly from disk — no push to GAR
required between changes.

**Prerequisites:** prepare the dev values file:
```bash
# Regenerate session keys if not already done
openssl rand -hex 32   # → app.sessionSecret
openssl rand -hex 16   # → app.sessionEncryptKey

# If the Oracle wallet was rotated, update the base64 values
base64 -w 0 resources-local-development-only/Wallet_devdb/cwallet.sso
base64 -w 0 resources-local-development-only/Wallet_devdb/ewallet.p12
```

**Install / upgrade:**
```bash
oc project safety-automation-shopping-demo

helm upgrade --install hoplon ./chart \
  -f resources-local-development-only/values.dev.yaml \
  -n safety-automation-shopping-demo
```

---

### Option B — From Google Artifact Registry (published release)

Use this to test or install the exact chart and image that customers receive. Both the
chart and the container image are pulled from GAR.

**Step 1 — Authenticate Helm to GAR** (once per shell session):
```bash
gcloud auth print-access-token | helm registry login \
  -u oauth2accesstoken --password-stdin \
  europe-west6-docker.pkg.dev
```

**Step 2 — Install / upgrade:**
```bash
helm upgrade --install hoplon \
  oci://europe-west6-docker.pkg.dev/automation-safety-485518/dynacov-safety-automation-public/chart/dynacov-safety-automation/hoplon \
  --version 0.1.1 \
  -f resources-local-development-only/values.dev.yaml \
  -n safety-automation-shopping-demo
```

> The `-f values.dev.yaml` override is still needed for the Oracle credentials and wallet.
> A customer would supply their own values file instead.

**Why the OCI path has two levels (`dynacov-safety-automation-public/dynacov-safety-automation`):**
When `helm push hoplon-0.1.1.tgz oci://.../dynacov-safety-automation-public/dynacov-safety-automation` is run, GAR stores the artifact
under `dynacov-safety-automation-public/<chart-name>` where `<chart-name>` comes from `Chart.yaml`. Since the
chart name is `hoplon`, the full pull path becomes `.../dynacov-safety-automation/dynacov-safety-automation`.

---

### Verify (both options)

```bash
# Check all resources were created
helm status hoplon -n safety-automation-shopping-demo

# Watch pod come up
oc get pods -l app.kubernetes.io/name=hoplon -n safety-automation-shopping-demo -w

# Check logs — first line should read "DynaCov Automation Safety v<version> is starting"
oc logs -f deployment/hoplon -n safety-automation-shopping-demo
```

## Updating configuration

Change values in your `values.dev.yaml` and run:

```bash
helm upgrade hoplon ./chart -f values.dev.yaml -n safety-automation-shopping-demo
```

Helm updates the ConfigMap/Secret and the checksum annotations trigger an automatic rolling
restart. No manual `oc rollout restart` needed.

## Adding a new environment variable (vendor workflow)

1. Add the env var key to `chart/templates/configmap.yaml` (non-sensitive) or
   `chart/templates/secret-credentials.yaml` (sensitive).
2. Add the corresponding field to `chart/values.yaml` with a sensible default.
3. If the field is required with no default, use the `required` helper:
   `{{ required "my.field is required" .Values.my.field | quote }}`
4. Bump `version` in `chart/Chart.yaml` (patch for backward-compatible additions,
   major for breaking changes that require customer action).
5. Document the new field in the values.yaml comment.

## Uninstalling

```bash
helm uninstall hoplon -n safety-automation-shopping-demo
```

This removes all chart-managed resources. The namespace itself is not deleted.

## Red Hat Marketplace certification notes

- The chart must pass `chart-verifier` — run locally with:
  `chart-verifier verify ./chart`
- All container images must be certified before chart submission.
- Submit the chart via a PR to `github.com/openshift-helm-charts/charts`.
- Required `Chart.yaml` annotations are already present:
  `charts.openshift.io/archs` and `charts.openshift.io/supportedOpenShiftVersions`.

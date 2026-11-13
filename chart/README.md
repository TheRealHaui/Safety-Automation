# DynaCov Safety Automation — OpenShift Resilience Testing Platform

DynaCov Safety Automation executes chaos and resilience scenario tests against OpenShift Deployments and
DeploymentConfigs. It comes with pre-configured test scenarios (pod kill, dependency latency, and more) and requires no cluster-wide or elevated privileges:
all permissions are namespace-scoped. Every test run streams logs live, embeds a live Grafana metrics
dashboard including automatic annotations, and produces a PDF audit report suitable for compliance and review purposes.

## Prerequisites

- OpenShift 4.12 or later
- Helm 3.x
- Istio / OpenShift Service Mesh (OSSM) with Prometheus scraping workload metrics
- An Oracle database accessible from the cluster (Easy Connect, Local Naming / TNS, or Oracle Cloud Autonomous Database with mTLS wallet)
- A Grafana instance (Grafana 10+) reachable from the cluster (with a Public organisation and a service account token set up as described in the Grafana setup section below)
- A DynaCov license file (`sa-key.json`) — required to pull the container image. A free trial is available — simply register at https://www.safetyautomation.ai/contact to receive yours.

## Image pull secret (license)

The DynaCov Safety Automation container image is distributed from a private registry. Access is controlled
by a GCP Service Account key file (`sa-key.json`) issued by DynaCov as part of the license.
A free trial license is available — simply register at https://www.safetyautomation.ai/contact to receive yours.

Before installing the chart, create the pull secret in your target namespace:

```bash
oc create secret docker-registry gar-pull-secret \
  --docker-server=europe-west6-docker.pkg.dev \
  --docker-username=_json_key \
  --docker-password="$(cat sa-key.json)" \
  -n <your-namespace>
```

> `_json_key` is not an actual username — it is a fixed string required by Google's registry
> authentication protocol to signal that the password field contains a JSON service account key.
> Always use exactly `_json_key`, regardless of the service account name.

Then reference the secret in the Helm install command via `--set image.pullSecrets[0].name=gar-pull-secret`
as shown in the installation examples below.

> **Air-gapped / private mirror installations**
> If you mirror the container image to an internal registry and manage registry
> authentication at the cluster level (cluster-wide pull secret or OpenShift
> `ImageContentSourcePolicy`), set `image.repository` to your mirror URL and omit
> `image.pullSecrets`. The chart enforces the pull secret requirement only when the
> default DynaCov registry is in use — overriding `image.repository` disables the check.

## Grafana setup

Grafana integration is required. DynaCov Safety Automation will not install without all three required Grafana parameters (`grafana.url`, `grafana.token`, `grafana.prometheusDatasourceUid`).
It is used for:

- Posting test-run annotations to the dashboard at run start and end (visible as vertical markers)
- Embedding the live metrics dashboard in the run detail page as an iframe
- Querying the Prometheus scrape interval at run startup to compute correct warm-up waits

**Step 1 — Create a Public organisation**

Grafana's default "Main Organisation" uses session cookies for access control, which
are blocked by browser SameSite policy when the iframe is served cross-origin from
OpenShift. A Public org allows anonymous iframe access without authentication.

1. Log in to Grafana as admin
2. Go to **Administration → Organizations → New org**
3. Name it e.g. `Public`
4. Switch into the new org (**user menu → Switch organization**)

> Note, DynaCov Safety Automation auto-discovers the numeric org ID at startup via the Grafana API — you do not need to supply it.

**Step 2 — Import the DynaCov Safety Automation dashboard**

The dashboard JSON is bundled with this chart at `grafana/hoplon-resilience-dashboard.json`.
Extract it from the chart archive or copy it from the repository.

1. While in the Public org, go to **Dashboards → Import**
2. Upload `grafana/hoplon-resilience-dashboard.json`
3. Select your Istio Prometheus datasource when prompted

**Step 3 — Create a service account token**

Annotations must be posted to the Public org. A Main Org. token silently misroutes them —
they appear to succeed in the logs but never appear on the dashboard.

1. Still in the Public org, go to **Administration → Service accounts → Add service account**
2. Set role to **Editor** (required to write annotations)
3. Add a token and copy it — it is shown only once

**Step 4 — Find the Prometheus datasource UID**

1. Go to **Connections → Data sources** and click your Istio Prometheus datasource
2. The UID is in the browser URL e.g. `/connections/datasources/edit/<uid>`

Once you have the token and datasource UID, use them in the installation commands below.

## Installation

Each example below is a complete, copy-paste-ready installation command. Choose the mode that matches your Oracle setup.

**Database connection mode 1 — Oracle Easy Connect (no config files):**
```bash
oc create secret docker-registry gar-pull-secret \
  --docker-server=europe-west6-docker.pkg.dev \
  --docker-username=_json_key \
  --docker-password="$(cat sa-key.json)" \
  -n <your-namespace>

helm upgrade --install dynacov-safety-automation \
  oci://europe-west6-docker.pkg.dev/automation-safety-485518/dynacov-safety-automation-public/dynacov-safety-automation \
  --set image.pullSecrets[0].name=gar-pull-secret \
  --set oracle.dsn="user/password@//host:1521/service_name" \
  --set app.sessionSecret="$(openssl rand -hex 32)" \
  --set app.sessionEncryptKey="$(openssl rand -hex 16)" \
  --set grafana.url="https://grafana.<namespace>.apps.<cluster-domain>" \
  --set grafana.token="<service-account-token>" \
  --set grafana.prometheusDatasourceUid="<datasource-uid>" \
  -n <your-namespace>
```

**Database connection mode 2 — Oracle TNS:**
```bash
oc create secret docker-registry gar-pull-secret \
  --docker-server=europe-west6-docker.pkg.dev \
  --docker-username=_json_key \
  --docker-password="$(cat sa-key.json)" \
  -n <your-namespace>

helm upgrade --install dynacov-safety-automation \
  oci://europe-west6-docker.pkg.dev/automation-safety-485518/dynacov-safety-automation-public/dynacov-safety-automation \
  --set image.pullSecrets[0].name=gar-pull-secret \
  --set oracle.dsn="user/password@myalias" \
  --set oracle.localNaming.enabled=true \
  --set oracle.localNaming.tnsnames="..." \
  --set oracle.localNaming.sqlnet="..." \
  --set app.sessionSecret="$(openssl rand -hex 32)" \
  --set app.sessionEncryptKey="$(openssl rand -hex 16)" \
  --set grafana.url="https://grafana.<namespace>.apps.<cluster-domain>" \
  --set grafana.token="<service-account-token>" \
  --set grafana.prometheusDatasourceUid="<datasource-uid>" \
  -n <your-namespace>
```

**Database connection mode 3 — Oracle TNS with mTLS wallet (for example for Oracle Cloud Autonomous Database):**
```bash
oc create secret docker-registry gar-pull-secret \
  --docker-server=europe-west6-docker.pkg.dev \
  --docker-username=_json_key \
  --docker-password="$(cat sa-key.json)" \
  -n <your-namespace>

helm upgrade --install dynacov-safety-automation \
  oci://europe-west6-docker.pkg.dev/automation-safety-485518/dynacov-safety-automation-public/dynacov-safety-automation \
  --set image.pullSecrets[0].name=gar-pull-secret \
  --set oracle.dsn="user/password@service_high" \
  --set oracle.localNaming.enabled=true \
  --set oracle.localNaming.tnsnames="..." \
  --set oracle.localNaming.sqlnet="SSL_SERVER_DN_MATCH=yes" \
  --set oracle.localNaming.wallet.enabled=true \
  --set oracle.localNaming.wallet.cwalletSso="<base64>" \
  --set oracle.localNaming.wallet.ewalletP12="<base64>" \
  --set app.sessionSecret="$(openssl rand -hex 32)" \
  --set app.sessionEncryptKey="$(openssl rand -hex 16)" \
  --set grafana.url="https://grafana.<namespace>.apps.<cluster-domain>" \
  --set grafana.token="<service-account-token>" \
  --set grafana.prometheusDatasourceUid="<datasource-uid>" \
  -n <your-namespace>
```

Use the **HTTPS external Route URL** for `grafana.url` when DynaCov Safety Automation is deployed on OpenShift
(e.g. `https://grafana-grafana.apps.<cluster-domain>`). OpenShift Routes are HTTPS by default,
and browsers silently block HTTP iframes inside HTTPS pages (mixed content). Because the Grafana
dashboard is embedded as an iframe, a plain `http://` URL will cause the panel to appear blank
with no error shown. The startup log will print a `WARN [Grafana probe]` warning if an `http://`
URL is detected.

### Verifying the setup

Check the DynaCov Safety Automation startup log for the Grafana probe result:
```bash
oc logs deployment/dynacov-safety-automation -n <namespace> | grep "Grafana"
```

A successful probe logs:
```
Grafana reachable at <url>, token accepted (HTTP 200), org ID auto-discovered: 2.
Grafana dashboard istio-workload-overview-version-two found and accessible.
```

Any `WARN [Grafana probe]` line identifies the specific misconfiguration — fix the
reported issue before running scenarios.

## Configuration

All required values must be supplied at install time. No sensitive defaults exist.

| Parameter | Description                                                                                                                                                                                                                                                                                   | Required |
|---|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---|
| `image.repository` | Container image registry path. Override only when mirroring the image to a private registry.                                                                                                                                                                                                  | No |
| `image.tag` | Image tag. Defaults to the chart release version; generally should not be overridden too.                                                                                                                                                                                                     | No |
| `image.pullSecrets` | List of pull secret names for the image registry (e.g. GAR Service Account secret). Required when using the default DynaCov registry — the chart aborts with a clear error at install time if omitted. Not required for air-gapped / mirrored installations that override `image.repository`. | Yes (default registry) / No (mirrored) |
| `oracle.dsn` | Oracle connection string — see Oracle connection modes below                                                                                                                                                                                                                                  | Yes |
| `oracle.localNaming.enabled` | Mount `tnsnames.ora` / `sqlnet.ora` into the pod. Required for Mode 2 (Local Naming) and Mode 3 (mTLS wallet)                                                                                                                                                                                 | No (default: `false`) |
| `oracle.localNaming.tnsnames` | Full content of `tnsnames.ora`                                                                                                                                                                                                                                                                | Required when `localNaming.enabled=true` |
| `oracle.localNaming.sqlnet` | Full content of `sqlnet.ora`                                                                                                                                                                                                                                                                  | Required when `localNaming.enabled=true` |
| `oracle.localNaming.wallet.enabled` | Also mount binary wallet files for mTLS. Required for Mode 3 (Oracle Cloud / TCPS)                                                                                                                                                                                                            | No (default: `false`) |
| `oracle.localNaming.wallet.cwalletSso` | Base64-encoded `cwallet.sso`                                                                                                                                                                                                                                                                  | Required when `wallet.enabled=true` |
| `oracle.localNaming.wallet.ewalletP12` | Base64-encoded `ewallet.p12`                                                                                                                                                                                                                                                                  | Required when `wallet.enabled=true` |
| `oracle.localNaming.wallet.ewalletPem` | Content of `ewallet.pem`                                                                                                                                                                                                                                                                      | No |
| `oracle.localNaming.wallet.ojdbcProperties` | Content of `ojdbc.properties`                                                                                                                                                                                                                                                                 | No |
| `app.sessionSecret` | 32-byte random string for session signing                                                                                                                                                                                                                                                     | Yes |
| `app.sessionEncryptKey` | 16/24/32-byte random string for session encryption                                                                                                                                                                                                                                            | Yes |
| `app.k6TimeoutSeconds` | Maximum duration of a K6 load test job in seconds                                                                                                                                                                                                                                             | No (default: `300`) |
| `app.kubeconfigPath` | Path to kubeconfig; leave empty to use the in-cluster service account                                                                                                                                                                                                                         | No |
| `grafana.url` | Grafana base URL. Must use `https://` when DynaCov Safety Automation is served over HTTPS — see the HTTPS note in the Installation section.                                                                                                                                                   | Yes |
| `grafana.token` | Grafana service account token (Public org, Editor permission)                                                                                                                                                                                                                                 | Yes |
| `grafana.prometheusDatasourceUid` | UID of the Istio Prometheus datasource in Grafana                                                                                                                                                                                                                                             | Yes |
| `grafana.skipTLSVerify` | Disable TLS certificate verification for all Grafana requests. For dev environments where Grafana uses a self-signed cluster CA not trusted by the local machine. **Never enable in production.**                                                                                             | No (default: `false`) |
| `route.enabled` | Create an OpenShift Route                                                                                                                                                                                                                                                                     | No (default: `true`) |
| `route.host` | Route hostname; leave empty for OpenShift auto-assignment                                                                                                                                                                                                                                     | No |
| `serviceAccount.name` | Name of the ServiceAccount created for the pod                                                                                                                                                                                                                                                | No (default: `dynacov-safety-automation-service-account`) |

## Oracle connection modes

DynaCov Safety Automation supports three Oracle connection modes. Choose the mode that matches your database setup.

### Mode 1 — Easy Connect (EZConnect)

No configuration files needed. Specify the host, port, and service name directly in the DSN.

```
oracle.dsn = user/password@//host:1521/service_name
```

`oracle.localNaming.enabled` must be `false` (default). Plain TCP, no TLS.

### Mode 2 — Local Naming (TNS)

Uses a `tnsnames.ora` alias resolved by the Oracle client. Required when the connection string
uses a short alias rather than a full host/port specification.

```
oracle.dsn                    = user/password@myalias
oracle.localNaming.enabled    = true
oracle.localNaming.tnsnames   = <full content of tnsnames.ora>
oracle.localNaming.sqlnet     = <full content of sqlnet.ora>
```

`oracle.localNaming.wallet.enabled` remains `false`. Plain TCP, no TLS.

### Mode 3 — mTLS / Wallet (Oracle Cloud Autonomous Database)

mTLS authentication layered on top of TNS naming. The `tnsnames.ora` uses `TCPS`/SSL parameters
and `sqlnet.ora` configures SSL; the binary wallet files provide the client certificate and trust
material. This is the mode required for Oracle Cloud Autonomous Database.

```
oracle.dsn                               = user/password@service_high
oracle.localNaming.enabled               = true
oracle.localNaming.tnsnames              = <content of tnsnames.ora from wallet download>
oracle.localNaming.sqlnet                = <content of sqlnet.ora from wallet download>
oracle.localNaming.wallet.enabled        = true
oracle.localNaming.wallet.cwalletSso     = <base64-encoded cwallet.sso>
oracle.localNaming.wallet.ewalletP12     = <base64-encoded ewallet.p12>
oracle.localNaming.wallet.ewalletPem     = <content of ewallet.pem>     (optional)
oracle.localNaming.wallet.ojdbcProperties = <content of ojdbc.properties> (optional)
```

> **Note:** `cwalletSso` and `ewalletP12` are binary files — pass them base64-encoded:
> ```bash
> --set oracle.localNaming.wallet.cwalletSso="$(base64 -w0 cwallet.sso)"
> --set oracle.localNaming.wallet.ewalletP12="$(base64 -w0 ewallet.p12)"
> ```

## Admin interface

DynaCov Safety Automation provides admin-only pages accessible from the navigation bar when logged in as an administrator.

| Page | URL | Description |
|---|---|---|
| User Management | `/admin/users` | Create, edit, and disable application users |
| Server Log | `/admin/logs` | View the current pod's application log (startup messages, connection status, warnings) |

The Server Log page shows the last 500 lines written to the application log since the pod started.
It is useful for verifying that Grafana and OpenShift connections succeeded at startup without
requiring cluster access to run `oc logs`. The buffer is in-memory and lost on pod restart;
use `oc logs` for historical entries.

## Uninstalling

```bash
helm uninstall dynacov-safety-automation -n <your-namespace>
```

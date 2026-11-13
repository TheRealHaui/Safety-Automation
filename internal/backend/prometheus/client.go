package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/grafana"
)

// defaultScrapeInterval is returned whenever the live config cannot be fetched.
// Verified against the kubernetes-pods job in this cluster's Prometheus via
// GET /api/v1/status/config — all standard jobs (kubernetes-pods,
// kubernetes-service-endpoints, prometheus) use 15s; only kubernetes-pods-slow
// uses 5m. If the Prometheus config ever changes, set GRAFANA_PROMETHEUS_DATASOURCE_UID
// so the live value is always read instead of relying on this constant.
const defaultScrapeInterval = 15 * time.Second

// FetchKubernetesPodsScrapeInterval queries the Prometheus config API and returns
// the scrape_interval configured for the kubernetes-pods scrape job.
//
// ── Why this matters ────────────────────────────────────────────────────────
// Istio sidecar metrics (istio_requests_total, etc.) live inside the Envoy
// process as in-memory counters. When a pod is killed those counters are gone.
// The new pod starts all counters at zero. Prometheus uses counter resets in
// rate() / increase() functions by detecting a drop and compensating — BUT only
// if it actually scraped the counter while it was non-zero. If Prometheus never
// got a scrape in between "K6 started" and "pod killed", those requests are
// permanently lost from the destination-side metrics and the Grafana dashboard
// shows nothing. On top of that metrics shall not get pushed to Prometheus as
// the LLMs say, Even if that might not be hundred true pushing metrics would
// need reasonable effort and/or bigger changes of the target OpenShift instance.
// We therefore use the wait two times plus two seconds Prometheus scrape
// interval approach. Alternative would be to increase scrape interval for
// certain Services using ServiceMonitor but with that if Prometheus used via
// Operator than that, if Prometheus cluster wide than that, if admin does not
// allow than over, admin can set silent ignores and this and that and this
// and that. Not a good approach. Maybe as custom this or that on top of this
// algorithm here but never as replacement. This because even with much lower
// scrape intervals our software has to wait.
//
// Additionally, rate() and irate() require at least two consecutive data points
// to compute a value. One scrape = one data point = dashboard panel stays blank.
//
// The fix: wait long enough before killing the pod for Prometheus to scrape at
// least twice. See PodKillAndStartScenario.go for the warmup calculation.
//
// ── Why via Grafana datasource proxy ────────────────────────────────────────
// The Istio Prometheus instance (prometheus-internal.istio-system.svc:9090) has
// no external OpenShift Route — it is only reachable from inside the cluster.
// During local development Safety Automation runs on a developer laptop and cannot reach
// that in-cluster URL directly. The Grafana datasource proxy forwards requests
// to Prometheus on behalf of the caller, so only the external Grafana URL is
// needed. GRAFANA_URL and GRAFANA_TOKEN are already required for annotations,
// so no new credentials are introduced — only GRAFANA_PROMETHEUS_DATASOURCE_UID
// is new (the datasource UID visible under Grafana → Connections → Data sources).
//
// Falls back silently to defaultScrapeInterval (15s) if:
//   - GRAFANA_URL, GRAFANA_TOKEN, or GRAFANA_PROMETHEUS_DATASOURCE_UID are unset
//   - The Grafana or Prometheus API call fails for any reason
//
// A 5-second context timeout is applied so an unreachable Grafana does not
// stall scenario startup.
func FetchKubernetesPodsScrapeInterval(ctx context.Context) time.Duration {
	grafanaURL := os.Getenv("GRAFANA_URL")
	grafanaToken := os.Getenv("GRAFANA_TOKEN")
	datasourceUID := os.Getenv("GRAFANA_PROMETHEUS_DATASOURCE_UID")

	if grafanaURL == "" || grafanaToken == "" || datasourceUID == "" {
		return defaultScrapeInterval
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/api/datasources/proxy/uid/%s/api/v1/status/config", grafanaURL, datasourceUID)
	req, err := http.NewRequestWithContext(ctxWithTimeout, http.MethodGet, url, nil)
	if err != nil {
		return defaultScrapeInterval
	}
	req.Header.Set("Authorization", "Bearer "+grafanaToken)

	resp, err := grafana.GrafanaHTTPClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return defaultScrapeInterval
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			YAML string `json:"yaml"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return defaultScrapeInterval
	}

	return parseKubernetesPodsScrapeInterval(result.Data.YAML)
}

// parseKubernetesPodsScrapeInterval extracts the scrape_interval for the
// kubernetes-pods job from the raw Prometheus config YAML string.
// It first looks for a job-specific override, then falls back to the global
// scrape_interval, then to defaultScrapeInterval.
func parseKubernetesPodsScrapeInterval(configYAML string) time.Duration {
	// Locate the kubernetes-pods job block: everything from "job_name: kubernetes-pods"
	// up to the next "- job_name:" entry (or end of string).
	jobStart := strings.Index(configYAML, "job_name: kubernetes-pods")
	if jobStart >= 0 {
		jobBlock := configYAML[jobStart:]
		if next := strings.Index(jobBlock[1:], "- job_name:"); next >= 0 {
			jobBlock = jobBlock[:1+next]
		}
		if d := firstScrapeInterval(jobBlock); d > 0 {
			return d
		}
	}

	// Fall back to the global scrape_interval.
	if d := firstScrapeInterval(configYAML); d > 0 {
		return d
	}

	return defaultScrapeInterval
}

// firstScrapeInterval finds the first "scrape_interval: <duration>" token in s
// and parses it with time.ParseDuration. Returns 0 on any parse failure.
func firstScrapeInterval(s string) time.Duration {
	const key = "scrape_interval:"
	idx := strings.Index(s, key)
	if idx < 0 {
		return 0
	}
	token := strings.TrimSpace(s[idx+len(key):])
	if end := strings.IndexAny(token, " \t\r\n"); end >= 0 {
		token = token[:end]
	}
	d, err := time.ParseDuration(token)
	if err != nil {
		return 0
	}
	return d
}

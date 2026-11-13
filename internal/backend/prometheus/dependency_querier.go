package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/grafana"
)

// Dependency represents an outbound service call discovered from Prometheus
// istio_requests_total metrics. All fields are populated directly from the
// Prometheus label set — no inference or guessing.
type Dependency struct {
	ServiceHostname   string // destination_service  (full FQDN, e.g. catalogue.ns.svc.cluster.local)
	ServiceNamespace  string // destination_service_namespace
	WorkloadName      string // destination_workload
	WorkloadNamespace string // destination_workload_namespace
}

// QueryOutboundDependencies queries Prometheus (via the Grafana datasource proxy)
// for all services that the given workload called outbound in the last elapsedSeconds.
//
// Why via the Grafana proxy: the Istio Prometheus instance has no external Route —
// it is only reachable inside the cluster. The Grafana proxy forwards on behalf of
// the caller, so only GRAFANA_URL + GRAFANA_TOKEN (already required for annotations)
// and GRAFANA_PROMETHEUS_DATASOURCE_UID (already used by FetchKubernetesPodsScrapeInterval)
// are needed. No new credentials are introduced.
//
// The query used:
//
//	sum by (destination_service, destination_service_namespace,
//	        destination_workload, destination_workload_namespace)
//	(rate(istio_requests_total{
//	    source_workload="<workloadName>",
//	    source_workload_namespace="<namespace>"
//	}[<elapsedSeconds>s]))
//
// Filtering by source_workload + source_workload_namespace scopes the result to the
// workload under test. The range window [elapsedSeconds s] covers everything the
// workload has called since K6 started — callers should pass max(15, elapsed) so the
// window always covers at least one Prometheus scrape interval.
//
// Entries where destination_workload is "unknown", "PassthroughCluster", or
// "BlackHoleCluster" are filtered out (Step 3 of the HttpDependencyDelayScenario plan):
//   - "unknown"            → external services not enrolled in Istio, or Envoy passthrough
//   - "PassthroughCluster" → catch-all for traffic that bypasses Istio's cluster resolution
//   - "BlackHoleCluster"   → traffic routed to a synthetic black hole (e.g. blocked egress)
//
// DNS traffic (UDP port 53) is never present — Istio does not intercept UDP.
func QueryOutboundDependencies(ctx context.Context, workloadName, namespace string, elapsedSeconds int) ([]Dependency, error) {
	grafanaURL := os.Getenv("GRAFANA_URL")
	grafanaToken := os.Getenv("GRAFANA_TOKEN")
	datasourceUID := os.Getenv("GRAFANA_PROMETHEUS_DATASOURCE_UID")

	if grafanaURL == "" || grafanaToken == "" || datasourceUID == "" {
		return nil, fmt.Errorf("GRAFANA_URL, GRAFANA_TOKEN, or GRAFANA_PROMETHEUS_DATASOURCE_UID not set — cannot query Prometheus for dependencies")
	}

	// PromQL: rate over a window that grows with K6 runtime so early-called dependencies
	// are never missed. The 15s floor matches the minimum Prometheus scrape interval.
	query := fmt.Sprintf(
		`sum by (destination_service, destination_service_namespace, destination_workload, destination_workload_namespace) (rate(istio_requests_total{source_workload=%q,source_workload_namespace=%q}[%ds]))`,
		workloadName, namespace, elapsedSeconds,
	)

	proxyURL := fmt.Sprintf(
		"%s/api/datasources/proxy/uid/%s/api/v1/query?query=%s",
		grafanaURL, datasourceUID, url.QueryEscape(query),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building Prometheus dependency query request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+grafanaToken)

	resp, err := grafana.GrafanaHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus via Grafana proxy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus query returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding Prometheus query response: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("Prometheus query status %q (expected \"success\")", result.Status)
	}

	var deps []Dependency
	for _, r := range result.Data.Result {
		destWorkload := r.Metric["destination_workload"]

		// Filter out non-real-workload entries (Step 3 of the HttpDependencyDelayScenario plan).
		// These represent traffic to external or synthetic endpoints that are not
		// enrolled in the Istio service mesh and therefore cannot be targeted with a
		// VirtualService fault rule.
		if destWorkload == "unknown" || destWorkload == "PassthroughCluster" || destWorkload == "BlackHoleCluster" {
			continue
		}

		svcHostname := r.Metric["destination_service"]
		if svcHostname == "" {
			continue
		}

		deps = append(deps, Dependency{
			ServiceHostname:   svcHostname,
			ServiceNamespace:  r.Metric["destination_service_namespace"],
			WorkloadName:      destWorkload,
			WorkloadNamespace: r.Metric["destination_workload_namespace"],
		})
	}
	return deps, nil
}

package grafana

import (
	"crypto/tls"
	"net/http"
)

// GrafanaHTTPClient is the shared HTTP client for all Grafana (and Grafana-proxied Prometheus)
// requests. Initialised by Init() in main.go before any scenario or probe runs.
// Falls back to http.DefaultClient if Init() is never called.
var GrafanaHTTPClient *http.Client = http.DefaultClient

// Init configures the shared HTTP client. Must be called once at application startup.
// When skipTLSVerify is true, TLS certificate verification is disabled — intended for
// development environments where the Grafana Route uses a self-signed cluster CA that
// is not trusted by the local machine. Never enable in production.
func Init(skipTLSVerify bool) {
	if skipTLSVerify {
		GrafanaHTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		}
		return
	}
	GrafanaHTTPClient = http.DefaultClient
}

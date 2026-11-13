package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Add annotations to Grafana that get shown in Dashboards that are configured to
// show annotations. annotationRequest is the payload sent to the Grafana annotations API.
type annotationRequest struct {
	Text         string   `json:"text"`
	Tags         []string `json:"tags"`
	Time         int64    `json:"time"` // Unix milliseconds
	DashboardUID string   `json:"dashboardUID,omitempty"`
}

// AddAnnotation posts a single annotation to Grafana using the GRAFANA_URL and
// GRAFANA_TOKEN environment variables.
// Grafana distinguishes two annotation scopes:
//   - Global annotations (DashboardUID left empty): appear on every dashboard
//     in the Grafana instance that has the "Annotations" feature enabled, which
//     is the default for all dashboards.
//   - Dashboard-scoped annotations (DashboardUID set): appear only on the
//     specific dashboard identified by that UID.
//
// We always set DashboardUID so annotations are scoped to the Safety Automation Resilience
// Dashboard and do not pollute every other dashboard in the instance.
// Errors are non-fatal — a missing or misconfigured Grafana URL is logged and
// silently ignored so the scenario itself is not interrupted.
func AddAnnotation(ctx context.Context, text string, tags []string, dashboardUID string) error {

	//todo
	//Improve later
	//Provide as
	//parameter here
	grafanaURL := os.Getenv("GRAFANA_URL")
	grafanaToken := os.Getenv("GRAFANA_TOKEN")

	if grafanaURL == "" || grafanaToken == "" {
		return fmt.Errorf("GRAFANA_URL or GRAFANA_TOKEN not set, skipping annotation")
	}

	payload := annotationRequest{
		Text:         text,
		Tags:         tags,
		Time:         time.Now().UnixMilli(),
		DashboardUID: dashboardUID,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal annotation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, grafanaURL+"/api/annotations", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create annotation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+grafanaToken)

	resp, err := GrafanaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to post annotation to Grafana: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Grafana annotations API returned HTTP %d", resp.StatusCode)
	}

	return nil
}

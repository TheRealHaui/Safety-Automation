package httpHandler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/auth"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/grafana"
	"github.com/TheRealHaui/Safety-Automation/internal/scenarioImplementation"
	"github.com/go-chi/chi/v5"
)

// grafanaURL gets read once at startup.
var grafanaURL = os.Getenv("GRAFANA_URL")

// ─── Start Test Run ───────────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) PostStartTestRun(w http.ResponseWriter, r *http.Request) {

	//Before we had here
	//if err := r.ParseForm(); err != nil {
	//However this did not work because
	//"The short version: your handler is fine for classic form posts (application/x-www-form-urlencoded). But your client sends multipart/form-data (because it uses FormData), and r.ParseForm() alone isn’t always enough to populate r.FormValue for multipart. That’s why values come back empty even though you see them in the payload.
	//Key differences
	//application/x-www-form-urlencoded
	//Body looks like: key=value&key2=value2
	//r.ParseForm() parses this reliably
	//r.FormValue("SCENARIO_TYPE_ID") works
	//multipart/form-data
	//Body is split into parts with boundaries
	//Needs multipart parsing
	//r.ParseForm() does not always populate r.Form for multipart unless ParseMultipartForm() is called
	//So r.FormValue(...) can return "" even though the payload has data
	//Why you see it
	//Your JS uses new FormData(form) → browser sends multipart/form-data
	//In Go, you must call r.ParseMultipartForm() for multipart
	//Without it, r.FormValue(...) can be empty
	//Two valid fixes
	//Client-side: send URL‑encoded instead of multipart (what I tried earlier)
	//Server-side: explicitly parse multipart and read from r.MultipartForm (what I just

	//Further, that r.ParseForm and r.ParseMultipartForm does not only verify
	//if there are problems but prepares the request for further processing
	//Without calling it subsequent r.FormValue(...) calls and so on would
	//not work. Important to keep in mind.
	if err := r.ParseMultipartForm(1000000); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	user := auth.UserFromContext(r.Context())

	workloadName := r.FormValue("workload_name")
	workloadNS := r.FormValue("workload_namespace")
	workloadKind := applicationModelStructs.OpenShiftWorkloadTypeStruct(r.FormValue("workload_kind"))
	scenarioIDStr := r.FormValue("SCENARIO_TYPE_ID")
	scenarioID, err := strconv.ParseInt(scenarioIDStr, 10, 64)

	if err != nil || scenarioIDStr == "" {

		if scenarioIDStr == "" {
			http.Error(w, "Test Runs must only get started with a valid scenario. The given scenario id however was empty.", http.StatusBadRequest)
		} else {
			http.Error(w, fmt.Sprintf("Test Runs must only get started with a valid scenario. The given scenario id however was not valid: \"%s\".", err),
				http.StatusBadRequest)
		}

		return

	}

	// Parse URLs
	urlStrings := r.Form["url_string"]
	httpMethods := r.Form["http_method"]
	requestBodies := r.Form["request_body"]
	contentTypes := r.Form["content_type"]
	//Minimum one
	//url provide
	if len(urlStrings) == 0 ||
		//Has to
		//have value
		(len(urlStrings) == 1 && urlStrings[0] == "") ||
		//HTTP Methods
		//same number
		(len(urlStrings) != len(httpMethods)) {
		http.Error(w, "must provide at least one URL with HTTP method", http.StatusBadRequest)
		return
	}
	var testURLs []database.TestRunURL
	//Ueberlegen gut
	//Nur URI
	for i, u := range urlStrings {
		if _, err = isValidClusterURL(u); err != nil {
			http.Error(w, fmt.Sprintf("Every url must be a valid cluster url. \"%s\" was however not", u), http.StatusBadRequest)
			return
		}
		var body, ct string
		if i < len(requestBodies) {
			body = strings.TrimSpace(requestBodies[i])
		}
		if i < len(contentTypes) {
			ct = strings.TrimSpace(contentTypes[i])
		}
		testURLs = append(testURLs, database.TestRunURL{
			URLString:   strings.TrimSpace(u),
			HTTPMethod:  httpMethods[i],
			RequestBody: body,
			ContentType: ct,
		})
	}

	// Get scenario struct name
	var goStructName string
	err = h.DB.QueryRowContext(r.Context(), `SELECT GO_STRUCT_NAME FROM TEST_SCENARIO_TYPE WHERE ID = :1`, scenarioID).Scan(&goStructName)
	if err != nil {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	scenario, err := scenarioImplementation.GetScenario(goStructName)
	if err != nil {
		http.Error(w, fmt.Sprintf("scenario not loaded: %v", err), http.StatusInternalServerError)
		return
	}

	// Create test run record
	run := &database.TestRun{
		ScenarioTypeID:    scenarioID,
		WorkloadName:      workloadName,
		WorkloadNamespace: workloadNS,
		WorkloadKind:      workloadKind,
		StartedBy:         fmt.Sprintf("%d", user.ID),
	}

	testRunID, err := database.GetTestRunID(r.Context(), h.DB, run)
	if err != nil {
		http.Error(w, "failed to create test run", http.StatusInternalServerError)
		return
	}

	log.Printf("Created test run with id: %d", testRunID)

	// Save URLs
	if err := database.InsertTestRunURLs(r.Context(), h.DB, testRunID, testURLs); err != nil {
		log.Printf("Insert of test run urls for rest run %d failed: %v", testRunID, err)
	}

	// Get workload details
	workload, err := h.KubernetesClient.GetSingleWorkloadDetails(r.Context(), workloadName, workloadKind)
	if err != nil {
		workload = &applicationModelStructs.Workload{Name: workloadName, Namespace: workloadNS, Kind: workloadKind}
	}

	// Enqueue
	if err := h.TestRunManager.Enqueue(testRunID, *workload, scenario, testURLs); err != nil {
		database.UpdateTestRunStatus(r.Context(), h.DB, testRunID, applicationModelStructs.StatusFailed, err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("HX-Redirect", fmt.Sprintf("/workloads/%s/runs/%d", workloadName, testRunID))
	w.WriteHeader(http.StatusAccepted)
}

// ─── Test Run Detail / SSE ────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) ShowTestRunDetail(w http.ResponseWriter, r *http.Request) {
	testRunID, err := strconv.ParseInt(chi.URLParam(r, "run_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	tr, err := database.GetTestRun(r.Context(), h.DB, testRunID)
	if err != nil {
		http.Error(w, "test run not found", http.StatusNotFound)
		return
	}
	urls, _ := database.GetTestRunURLs(r.Context(), h.DB, testRunID)
	logs, _ := database.GetLogsForRun(r.Context(), h.DB, testRunID, nil)
	isActive := h.TestRunManager.IsActive(testRunID)

	// Grafana time window padding:
	// We add a 30 seconds buffer before the run start and after the run end so that
	// metrics scraped slightly outside the exact run boundaries are still visible.
	// Without this, Grafana shows "Data outside time range" for data points that
	// fall just outside the pinned window. The template cannot do time arithmetic,
	// so we compute the padded epoch-millisecond values here and pass them in.
	const grafanaPadding = 30 * time.Second
	grafanaFrom := fmt.Sprintf("%d", tr.StartedAt.Add(-grafanaPadding).UnixMilli())
	var grafanaTo string
	if isActive {
		// Active run: keep the right edge live so the dashboard updates in real time.
		grafanaTo = "now"
	} else if tr.FinishedAt != nil {
		grafanaTo = fmt.Sprintf("%d", tr.FinishedAt.Add(grafanaPadding).UnixMilli())
	} else {
		grafanaTo = "now"
	}

	data := map[string]any{
		"Run":         tr,
		"URLs":        urls,
		"Logs":        logs,
		"IsActive":    isActive,
		"GrafanaFrom": grafanaFrom,
		"GrafanaTo":   grafanaTo,
		"GrafanaURL":  grafanaURL,
		// DashboardUID and orgID are owned by the
		// grafana package — dashboardUID is a vendor constant,
		// OrgID is auto-discovered via GET /api/org at startup
		// so the administrator does not need to supply it.
		"GrafanaDashboardUID": grafana.DashboardUID,
		"GrafanaOrgID":        grafana.OrgID,
	}
	h.render(w, r, "run_detail_template", data)
}

// SSELogs streams live log entries for an active test run via Server-Sent Events.
func (h *SharedHTTPRequestDependenciesHandlerStruct) SSELogs(w http.ResponseWriter, r *http.Request) {
	testRunID, err := strconv.ParseInt(chi.URLParam(r, "run_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, active := h.TestRunManager.Subscribe(testRunID)
	if !active {
		// Run is not active – send any existing DB logs and a "done" event
		logs, _ := database.GetLogsForRun(r.Context(), h.DB, testRunID, nil)
		for _, l := range logs {
			fmt.Fprintf(w, "data: %s\n\n", sseLogLine(l))
			flusher.Flush()
		}
		fmt.Fprintf(w, "event: done\ndata: finished\n\n")
		flusher.Flush()
		return
	}
	defer h.TestRunManager.Unsubscribe(testRunID, ch)

	// Heartbeat keeps the SSE connection alive through OpenShift's HAProxy Route,
	// which cuts idle HTTP connections after its timeout (default 30s).
	//A ": heartbeat" SSE comment resets the idle timer without affecting the
	//client-side EventSource.
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case entry, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "event: done\ndata: finished\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", sseLogLine(entry))
			flusher.Flush()
		}
	}
}

func sseLogLine(l database.TestRunLog) string {
	b, _ := json.Marshal(map[string]string{
		"ts":      l.LogTimestamp.Format("15:04:05.000"),
		"level":   l.LogLevel,
		"message": l.Message,
	})
	return string(b)
}

// CancelTestRun cancels an active test run.
func (h *SharedHTTPRequestDependenciesHandlerStruct) CancelTestRun(w http.ResponseWriter, r *http.Request) {
	testRunID, err := strconv.ParseInt(chi.URLParam(r, "run_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	if err := h.TestRunManager.Cancel(testRunID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("HX-Redirect", fmt.Sprintf("/runs/%d", testRunID))
	w.WriteHeader(http.StatusOK)
}

// ─── All Test Runs View ───────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) ShowAllTestRuns(w http.ResponseWriter, r *http.Request) {

	req := pageReqFromQuery(r)
	result, err := database.ListTestRuns(r.Context(), h.DB, req, "", "")

	if err != nil {
		log.Printf("list test runs: %v", err)
		result = &applicationModelStructs.PageResult[database.TestRun]{}
	}

	data := map[string]any{
		"Result": result,
		"Req":    req,
	}

	if r.Header.Get("HX-Request") == "true" {
		h.partial(w, "runs_table_template", data)
		return
	}

	h.render(w, r, "runs_template", data)

}

func isValidClusterURL(raw string) (bool, error) {
	u, err := url.ParseRequestURI(raw)

	if err != nil {
		return false, err
	}

	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false, errors.New("Schema must be http or https")
	}

	if u.Host == "" {
		return false, errors.New("Host not set.")
	}

	return true, nil
}

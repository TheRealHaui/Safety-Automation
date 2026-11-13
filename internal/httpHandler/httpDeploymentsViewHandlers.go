package httpHandler

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

// ─── Deployments View ─────────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) ShowAllDeployments(w http.ResponseWriter, r *http.Request) {

	workloads, err := h.KubernetesClient.GetAllWorkloads(r.Context())

	var remoteOpenShiftReached bool = false

	if err != nil {
		log.Printf("Retrieving workloads from OpenShift failed: %v", err)
		workloads = []applicationModelStructs.Workload{}
	} else {
		remoteOpenShiftReached = true
	}

	// Fetch all test run workload names to know which have runs
	rows, _ := h.DB.QueryContext(r.Context(), `SELECT DISTINCT WORKLOAD_NAME, WORKLOAD_NAMESPACE FROM TEST_RUN`)
	withRuns := map[string]bool{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var n, ns string
			rows.Scan(&n, &ns)
			withRuns[n+"|"+ns] = true
		}
	}

	// Apply filter
	onlyWithRuns := r.URL.Query().Get("only_with_runs") == "1"
	search := strings.ToLower(r.URL.Query().Get("search"))
	sortBy := r.URL.Query().Get("sort_by")
	sortDir := r.URL.Query().Get("sort_dir")

	var filtered []applicationModelStructs.Workload
	for _, w := range workloads {
		if onlyWithRuns && !withRuns[w.Name+"|"+w.Namespace] {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(w.Name), search) {
			continue
		}
		filtered = append(filtered, w)
	}

	// Sort
	sortWorkloads(filtered, sortBy, sortDir)

	// Paging
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize := 20
	total := len(filtered)
	totalPages := (total + pageSize - 1) / pageSize
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	var paged []applicationModelStructs.Workload
	if start < total {
		paged = filtered[start:end]
	}

	data := map[string]any{
		"Workloads":              paged,
		"WithRuns":               withRuns,
		"OnlyWithRuns":           onlyWithRuns,
		"Search":                 r.URL.Query().Get("search"),
		"SortBy":                 sortBy,
		"SortDir":                sortDir,
		"Page":                   page,
		"PageSize":               pageSize,
		"TotalPages":             totalPages,
		"TotalCount":             total,
		"Namespace":              h.KubernetesClient.Namespace,
		"RemoteOpenShiftReached": remoteOpenShiftReached,
	}

	if r.Header.Get("HX-Request") == "true" {
		h.partial(w, "deployments_table_template", data)
		return
	}

	h.render(w, r, "deployments_template", data)

}

func sortWorkloads(ws []applicationModelStructs.Workload, sortBy, sortDir string) {
	// Simple bubble sort for brevity; workload lists are typically small
	asc := sortDir != "desc"
	for i := 0; i < len(ws)-1; i++ {
		for j := 0; j < len(ws)-i-1; j++ {
			var less bool
			switch sortBy {
			case "kind":
				less = string(ws[j].Kind) < string(ws[j+1].Kind)
			case "replicas":
				less = ws[j].Replicas < ws[j+1].Replicas
			default:
				less = ws[j].Name < ws[j+1].Name
			}
			if (asc && !less) || (!asc && less) {
				ws[j], ws[j+1] = ws[j+1], ws[j]
			}
		}
	}
}

package httpHandler

import (
	"fmt"
	"log"
	"net/http"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/go-chi/chi/v5"
)

// ─── Workload Detail View ─────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) ShowWorkloadDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	kindStr := r.URL.Query().Get("kind")

	workloadKind := applicationModelStructs.OpenShiftWorkloadTypeStruct(kindStr)

	if workloadKind.IsValid() == false {

		var errorStsring string = fmt.Sprintf("Workload kind \"%s\" is not a valid workload kind.", kindStr)

		log.Print(errorStsring)
		http.Error(w, errorStsring, http.StatusNotAcceptable)

		return

	}

	workload, err := h.KubernetesClient.GetSingleWorkloadDetails(r.Context(), name, workloadKind)
	if err != nil {
		log.Printf("get workload %s: %v", name, err)
		http.Error(w, "Workload not found", http.StatusNotFound)
		return
	}

	// Fetch previous test runs for this workload
	req := pageReqFromQuery(r)
	runs, err := database.ListTestRuns(r.Context(), h.DB, req, name, h.KubernetesClient.Namespace)
	if err != nil {
		log.Printf("list test runs: %v", err)
		runs = &applicationModelStructs.PageResult[database.TestRun]{}
	}

	scenarios, err := database.ListScenarios(r.Context(), h.DB)
	if err != nil {
		log.Printf("list scenarios: %v", err)
	}

	//todo
	//Currently direct assignment here
	//to show resource stress scenario
	//Of course improve later
	filtered := scenarios[:0]
	for _, s := range scenarios {
		if s.ScenarioName != "Resource Stress" {
			filtered = append(filtered, s)
		}
	}
	scenarios = filtered

	data := map[string]any{
		"Workload":  workload,
		"TestRuns":  runs,
		"Scenarios": scenarios,
	}
	h.render(w, r, "workload_detail_template", data)
}

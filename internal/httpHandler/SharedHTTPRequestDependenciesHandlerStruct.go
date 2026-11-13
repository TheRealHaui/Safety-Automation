package httpHandler

import (
	"database/sql"
	"html/template"
	"log"
	"net/http"

	"github.com/TheRealHaui/Safety-Automation/internal/auth"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/serverlog"
	"github.com/TheRealHaui/Safety-Automation/internal/testRunExecution"
	"github.com/TheRealHaui/Safety-Automation/internal/version"
)

// SharedHTTPRequestDependenciesHandlerStruct holds shared dependencies for all HTTP handlers.
type SharedHTTPRequestDependenciesHandlerStruct struct {
	DB               *sql.DB
	KubernetesClient *restClient.KubernetesClient
	TestRunManager   *testRunExecution.TestRunScenarioExecutionManager
	HTMLTemplates    map[string]*template.Template
	ServerLog        *serverlog.RingBuffer
}

func (h *SharedHTTPRequestDependenciesHandlerStruct) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	type pageData struct {
		User    *database.User
		Data    any
		Version string
	}

	pd := pageData{
		User:    auth.UserFromContext(r.Context()),
		Data:    data,
		Version: version.Display,
	}
	tmpl := h.HTMLTemplates[name]
	if tmpl == nil {
		log.Printf("template %s not found", name)
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, name, pd); err != nil {
		log.Printf("template %s error: %v", name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (h *SharedHTTPRequestDependenciesHandlerStruct) partial(w http.ResponseWriter, name string, data any) {
	tmpl := h.HTMLTemplates[name]
	if tmpl == nil {
		log.Printf("template %s not found", name)
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("partial %s error: %v", name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

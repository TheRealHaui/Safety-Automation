package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/auth"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/grafana"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/config"
	"github.com/TheRealHaui/Safety-Automation/internal/httpHandler"
	"github.com/TheRealHaui/Safety-Automation/internal/serverlog"
	"github.com/TheRealHaui/Safety-Automation/internal/testRunExecution"
	"github.com/TheRealHaui/Safety-Automation/internal/version"
	"github.com/TheRealHaui/Safety-Automation/web/templates"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {

	// Wire the in-memory ring buffer into the standard logger before any log call so that
	// every startup message (Grafana probe, OpenShift connection, DB migration result) is
	// captured and visible on the /admin/logs page without requiring cluster access.
	serverLogBuffer := &serverlog.RingBuffer{}
	log.SetOutput(io.MultiWriter(os.Stdout, serverLogBuffer))

	// First log line — printed before any other initialisation so it always appears
	// even if a subsequent step (DB, kubeconfig) fatals. version.Display is already
	// assembled by the time main() runs because Go's init() functions execute first.
	log.Printf("DynaCov Safety Automation v%s is starting", version.Display)

	//This loads the .evn file
	//or reads all values from
	//the secretes of the Helm
	//chart. Supposed to work
	//for local development with
	//make run and with Helm
	//Chart on OpenShift directly.
	//Note, this has to work with
	//the TNS_ADMIN shell variable
	//and SQL*Plus initialization
	//without any problem too.
	//This because TNS_ADMIN
	//shell variable value and
	//proper SQL*Plus start up
	//is tricky and specific.
	cfg := config.Load()

	// ── Database ─────────────────────────────────────────────────────────────
	if cfg.OracleDSN == "" {
		log.Fatal("ORACLE_DSN environment variable is required. Did you start the program using \"make run\"? Do not use \"go run ...\" directly.")
	}

	//Important to troubleshoot on
	//first installation in OpenShift
	log.Println("Going to open database connection.")

	mainDatabaseConnection, err := database.Open(cfg.OracleDSN)

	//Important to troubleshoot on
	//first installation in OpenShift
	log.Println("Successfully opened database connection.")

	if err != nil {
		log.Fatalf("cannot open main DB: %v", err)
	}
	defer mainDatabaseConnection.Close()

	// Separate DB connection for autonomous log writes
	logDatabaseConnection, err := database.Open(cfg.OracleDSN)
	if err != nil {
		log.Fatalf("cannot open log DB: %v", err)
	}
	logDatabaseConnection.SetMaxOpenConns(5)
	defer logDatabaseConnection.Close()

	if err := database.Migrate(mainDatabaseConnection); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	// ── Auth seed ─────────────────────────────────────────────────────────────
	auth.Init(cfg.SessionSecret, cfg.SessionEncryptKey)
	if err := auth.SeedAdminToUserTableIfNoUserExistsYet(mainDatabaseConnection); err != nil {
		log.Printf("WARN: could not seed admin user: %v", err)
	}

	// ── Scenario seed ─────────────────────────────────────────────────────────
	scenarios := []database.TestScenarioTypeStruct{
		{ScenarioName: "Pod Kill And Start", GoStructName: "PodKillAndStartScenario"},
		{ScenarioName: "HTTP Dependency Delay", GoStructName: "HttpDependencyDelayScenario"},
		{ScenarioName: "HTTP Dependency Error", GoStructName: "HttpDependencyErrorScenario"},
		{ScenarioName: "Resource Stress", GoStructName: "ResourceStressScenario"},
	}

	if err := database.SaveNonExistingScenariosToDatabase(context.Background(), mainDatabaseConnection, scenarios); err != nil {
		log.Printf("WARN: scenario seed: %v", err)
	}

	// ── Finding type seed ─────────────────────────────────────────────────────
	// Finding types are referenced by the scenario they belong to via SCENARIO_TYPE_ID.
	podKillScenarioTypeID, err := database.GetScenarioTypeIDByGoStructName(context.Background(), mainDatabaseConnection, "PodKillAndStartScenario")
	if err != nil {
		log.Printf("WARN: finding type seed: could not look up PodKillAndStartScenario type ID: %v", err)
	} else {
		findingTypes := []database.TestRunFindingType{
			{ScenarioTypeID: podKillScenarioTypeID, FindingTypeCode: "POD_RECOVERY_READY_PASSED_NO_LOAD_TIME_SECONDS", FindingTypeName: "Pod Recovery Time/Readiness probe succeeded without K6 load (seconds)"},
			{ScenarioTypeID: podKillScenarioTypeID, FindingTypeCode: "POD_RECOVERY_READY_PASSED_UNDER_LOAD_TIME_SECONDS", FindingTypeName: "Pod Recovery Time/Readiness probe succeeded under K6 load (seconds)"},
		}
		if err := database.SaveNonExistingFindingTypesToDatabase(context.Background(), mainDatabaseConnection, findingTypes); err != nil {
			log.Printf("WARN: finding type seed: %v", err)
		}
	}

	// ── OpenShift client ──────────────────────────────────────────────────────
	kubeconfigPath := cfg.KubeconfigPath

	kubernetesClient, err := restClient.New(kubeconfigPath)

	if err != nil {
		log.Fatalf("cannot create OpenShift client: %v", err)
	}

	log.Printf("OpenShift client created successfully using provided credentials.")

	//kubernetesClient.Namespace

	// ── Orphan VirtualService scan removal ────────────────────────────────────────────
	scanForOrphanVirtualServicesAndRemoveThemIfAppropriate(kubernetesClient)

	// ── Grafana HTTP client ───────────────────────────────────────────────────
	grafana.Init(cfg.GrafanaSkipTLSVerify)

	// ── Grafana probe ─────────────────────────────────────────────────────────
	// Runs at startup to surface misconfigured or unreachable Grafana early —
	// especially on OpenShift where the .env file is not present and env vars
	// must be injected via oc set env / ConfigMap / Secret. If this probe fails,
	// annotations will be silently skipped at runtime without any obvious error.
	probeGrafanaConnection()

	if cfg.GrafanaSkipTLSVerify {
		log.Println("WARN: GRAFANA_SKIP_TLS_VERIFY=true — TLS certificate verification is disabled for all Grafana requests that use https://. Do not use in production.")
	}

	openShiftServerVersion, err := kubernetesClient.GetOpenShiftServerVersion()

	if err != nil {
		log.Printf("No connection to OpenShift cluster. Application will resume however. " +
			"Please fix to no only see historic data but accomplish things in OpenShift too.")
	} else {
		log.Printf("Connected to OpenShift cluster version %s, active namespace: %s", openShiftServerVersion.String(), kubernetesClient.Namespace)
	}

	// ── Job manager ───────────────────────────────────────────────────────────
	manager := testRunExecution.New(
		mainDatabaseConnection, logDatabaseConnection, kubernetesClient,
		cfg.MaxParallelTests,
		time.Duration(cfg.K6TimeoutSeconds)*time.Second,
	)

	// ── Templates ────────────────────────────────────────────────────────────
	//That templates variable here actually is a package name
	//of one of the Go standard packages
	//To keep in mind
	//And the defined functions add and sub get used
	//inside the html template files
	//Look it up there
	//For pagination and other
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
	}

	//The templates are NOT
	//the files but the defined
	//and named templates in
	//the files like deploymens.html
	goTemplatesMap, err := loadTemplates(funcMap)

	if err != nil {
		log.Fatalf("parsing templates: %v", err)
	}

	// ── HTTP handler ─────────────────────────────────────────────────────────
	httpHandlerPointer := &httpHandler.SharedHTTPRequestDependenciesHandlerStruct{
		DB:               mainDatabaseConnection,
		KubernetesClient: kubernetesClient,
		TestRunManager:   manager,
		HTMLTemplates:    goTemplatesMap,
		ServerLog:        serverLogBuffer,
	}

	// ── Router ────────────────────────────────────────────────────────────────
	chiRouter := chi.NewRouter()
	// middleware.Logger hardcodes log.New(os.Stdout, ...) internally, bypassing
	// log.SetOutput and therefore the ring buffer used to show log messages in
	// the standard application log output and in the web page under /admin/logs.
	// Using RequestLogger with our own log.Logger that shares the same
	// io.MultiWriter ensures HTTP access log lines appear on /admin/logs
	// alongside application log lines. NoColor: true avoids ANSI escape codes
	// being stored in the ring buffer and  rendered as literal escape characters
	// in the web UI.
	chiRouter.Use(middleware.RequestLogger(&middleware.DefaultLogFormatter{
		Logger:  log.New(io.MultiWriter(os.Stdout, serverLogBuffer), "", log.LstdFlags),
		NoColor: true,
	}))
	chiRouter.Use(middleware.Recoverer)
	chiRouter.Use(middleware.RequestID)
	chiRouter.Use(auth.Middleware)

	// Liveness probe — no auth, no DB check.
	// Only answers "is the HTTP server up and not deadlocked?". Kubernetes restarts
	// the container when this fails, so it must never depend on external systems:
	// restarting the pod cannot fix a DB outage and would cause unnecessary churn.
	chiRouter.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Readiness probe — no auth, DB ping required.
	// Answers "can this pod serve real traffic right now?". Kubernetes removes the pod
	// from Service endpoints when this fails, preventing users from hitting a pod that
	// cannot reach the database. Uses the main DB connection (not a new one) so the
	// probe reflects the actual connection state the app is using.
	chiRouter.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := mainDatabaseConnection.PingContext(ctx); err != nil {
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Auth
	chiRouter.Get("/login", httpHandlerPointer.GetLogin)
	chiRouter.Post("/login", httpHandlerPointer.PostLogin)
	chiRouter.Post("/logout", httpHandlerPointer.PostLogout)

	// Admin-only routes — requires IS_ADMIN = 'Y' in addition to being logged in.
	// auth.Middleware (applied globally above) already guarantees an authenticated
	// session exists; this sub-router adds the admin check on top.
	chiRouter.Route("/admin", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user := auth.UserFromContext(r.Context())
				if user == nil || !user.IsAdmin {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		r.Get("/users", httpHandlerPointer.GetUsers)
		r.Get("/users/new", httpHandlerPointer.GetNewUser)
		r.Post("/users", httpHandlerPointer.PostCreateUser)
		r.Get("/users/{id}/edit", httpHandlerPointer.GetEditUser)
		r.Post("/users/{id}", httpHandlerPointer.PostUpdateUser)
		r.Get("/logs", httpHandlerPointer.GetAdminLogs)
	})

	// Profile
	chiRouter.Get("/profile", httpHandlerPointer.GetProfile)
	chiRouter.Post("/profile/change-password", httpHandlerPointer.PostChangePassword)

	// Workloads (Deployments) view
	chiRouter.Get("/", httpHandlerPointer.ShowAllDeployments)
	chiRouter.Get("/workloads", httpHandlerPointer.ShowAllDeployments)
	chiRouter.Get("/workloads/{name}", httpHandlerPointer.ShowWorkloadDetail)

	// Test run detail (per-workload)
	chiRouter.Get("/workloads/{name}/runs/{run_id}", httpHandlerPointer.ShowTestRunDetail)

	// All test runs
	chiRouter.Get("/runs", httpHandlerPointer.ShowAllTestRuns)
	chiRouter.Get("/runs/{run_id}", httpHandlerPointer.ShowTestRunDetail)
	chiRouter.Get("/runs/{run_id}/pdf", httpHandlerPointer.DownloadPDF)

	// API endpoints (used by JS/htmx)
	chiRouter.Post("/api/runs/start", httpHandlerPointer.PostStartTestRun)
	chiRouter.Post("/api/runs/{run_id}/cancel", httpHandlerPointer.CancelTestRun)
	chiRouter.Get("/api/runs/{run_id}/logs/stream", httpHandlerPointer.SSELogs)

	// ── Server ────────────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + cfg.AppPort,
		Handler:      chiRouter,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 = no timeout (needed for SSE)
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("Safety Automation started on :%s", cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
		log.Printf("Safety Automation started.")
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down…")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	// Close DB connections separately
	if err := mainDatabaseConnection.Close(); err != nil {
		log.Printf("main DB close: %v", err)
	}
	if err := logDatabaseConnection.Close(); err != nil {
		log.Printf("log DB close: %v", err)
	}

	log.Println("Shutdown complete")
}

func loadTemplates(funcMap template.FuncMap) (map[string]*template.Template, error) {

	baseTemplate := "base.html"
	templatesReturnMap := map[string]*template.Template{}

	//This reads
	//the file
	deploymentsT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "deployments.html")
	if err != nil {
		return nil, fmt.Errorf("deployments templates: %w", err)
	}
	//This registers the Go templates to
	//be part of the deployments_template file
	//Note that obviously the Go standard is
	//to give the templates the same name as
	//the html file has. So the template
	//deployments.html is inside the file
	//deployments.html. Very smart and clean.
	templatesReturnMap["deployments_template"] = deploymentsT
	templatesReturnMap["deployments_table_template"] = deploymentsT

	runsT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "runs.html")
	if err != nil {
		return nil, fmt.Errorf("runs templates: %w", err)
	}
	templatesReturnMap["runs_template"] = runsT
	templatesReturnMap["runs_table_template"] = runsT

	workloadDetailT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "workload_detail.html")
	if err != nil {
		return nil, fmt.Errorf("workload_detail templates: %w", err)
	}
	templatesReturnMap["workload_detail_template"] = workloadDetailT

	runDetailT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "run_detail.html")
	if err != nil {
		return nil, fmt.Errorf("run_detail templates: %w", err)
	}
	templatesReturnMap["run_detail_template"] = runDetailT

	loginT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "login.html")
	if err != nil {
		return nil, fmt.Errorf("login templates: %w", err)
	}
	templatesReturnMap["login_template"] = loginT

	profileT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "profile.html")
	if err != nil {
		return nil, fmt.Errorf("profile templates: %w", err)
	}
	templatesReturnMap["profile_template"] = profileT

	usersT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "users.html")
	if err != nil {
		return nil, fmt.Errorf("users templates: %w", err)
	}
	templatesReturnMap["users_template"] = usersT
	templatesReturnMap["users_table_template"] = usersT

	userFormT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "user_form.html")
	if err != nil {
		return nil, fmt.Errorf("user_form templates: %w", err)
	}
	templatesReturnMap["user_form_template"] = userFormT

	adminLogsT, err := template.New("").Funcs(funcMap).ParseFS(templates.ApplicationStartFileSystem, baseTemplate, "admin_logs.html")
	if err != nil {
		return nil, fmt.Errorf("admin_logs templates: %w", err)
	}
	templatesReturnMap["admin_logs_template"] = adminLogsT

	return templatesReturnMap, nil
}

// scanForOrphanVirtualServicesAndRemoveThemIfAppropriate deletes any Safety-Automation-managed VirtualServices left from
// a previous run that crashed or was forcibly killed before cleanup could execute.
// A leftover VirtualService would silently inject latency into live traffic until
// manually discovered and removed — an unacceptable silent failure mode.
//
// Safety gate: if another Safety Automation instance is already Running in the same namespace
// (e.g. during a rolling update), the scan is skipped entirely. Those VSes may
// belong to an active scenario on the other instance — deleting them would silently
// abort that run without any error feedback to the user. This situation should never
// happen in normal operation (Deployment is always replicas=1) but is guarded here
// as a safety net.
//
// ── Scope limitation: patched VSes are NOT covered by this scan ──────────────
//
// This scan only finds and deletes VSes that Safety Automation created from scratch
// (identified by the hoplon.io/managed-by=hoplon label). When HttpDependencyDelayScenario
// patches a pre-existing application-owned VirtualService (Case 2), that VS is
// intentionally NOT labelled with hoplon.io/managed-by=hoplon — doing so would
// cause this scan to delete the application team's VS entirely, permanently
// destroying their routing configuration.
//
// Consequence: if Safety Automation crashes while a patched VS is active, the 2-second fault
// delay remains in effect until an operator manually restores the original VS.
// A proper durable-restore path (save original to a ConfigMap before patching,
// restore from it here on startup) is tracked in: https://trello.com/c/oPZNF3VW
func scanForOrphanVirtualServicesAndRemoveThemIfAppropriate(kubernetesClient *restClient.KubernetesClient) {
	orphanCtx, orphanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	otherInstances, checkErr := kubernetesClient.CountOtherSafetyAutomationRunningPods(orphanCtx, kubernetesClient.Namespace)
	orphanCancel()

	if checkErr != nil {
		log.Printf("WARN: could not check for concurrent Safety Automation instances in namespace %q — skipping orphan VirtualService scan to be safe: %v",
			kubernetesClient.Namespace, checkErr)
		return
	}
	if otherInstances > 0 {
		log.Printf("Skipping orphan VirtualService scan: %d other Safety Automation instance(s) detected in namespace %q. "+
			"Those VirtualServices may belong to an active scenario run on the other instance.",
			otherInstances, kubernetesClient.Namespace)
		return
	}

	scanCtx, scanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	orphans, orphanErr := kubernetesClient.ListSafetyAutomationVirtualServiceNames(scanCtx, kubernetesClient.Namespace)
	scanCancel()

	if orphanErr != nil {
		log.Printf("WARN: orphan VirtualService scan failed (non-fatal): %v", orphanErr)
		return
	}
	if len(orphans) == 0 {
		log.Printf("Orphan VirtualService scan complete — no orphans found.")
		return
	}

	log.Printf("WARN: found %d orphaned Safety Automation VirtualService(s) from a previous run — deleting: %v", len(orphans), orphans)
	for _, vsName := range orphans {
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if delErr := kubernetesClient.DeleteVirtualService(delCtx, vsName, kubernetesClient.Namespace); delErr != nil {
			log.Printf("WARN: could not delete orphaned VirtualService %q: %v", vsName, delErr)
		} else {
			log.Printf("Deleted orphaned VirtualService %q successfully.", vsName)
		}
		delCancel()
	}
}

// probeGrafanaConnection verifies that GRAFANA_URL and GRAFANA_TOKEN are set and
// NOTE:
// On purpose this routine does not make application start fail in case of problems.
// This because the application shall work for historic data look up only too.
//
// TODO: receive these from cfg instead of os.Getenv — https://trello.com/c/IcypCnrh
// that the Grafana API is reachable and accepts the token. Called once at startup.
//
// This is a diagnostic probe only — failure is logged as a warning and the
// application continues. Grafana annotations are non-critical; the scenario
// execution itself is not affected.
//
// On OpenShift the .env file is not present in the pod, so env vars must be
// injected via `oc set env`, a ConfigMap, or a Secret. If either var is missing
// or the Grafana API returns an error, annotations will be silently skipped at
// runtime — this probe makes that failure visible in the pod logs at startup.
//
// TODO: Surface probe failures as a visible red warning banner in the website
// header (next to the navigation menu) so operators notice misconfiguration
// without having to dig through pod logs. This requires persisting the probe
// result (e.g. in a package-level var or a small in-memory config struct) and
// reading it in the base HTML template to conditionally render the banner.
// See https://trello.com/c/RAUS5V8I for details.
func probeGrafanaConnection() {
	grafanaURL := os.Getenv("GRAFANA_URL")
	grafanaToken := os.Getenv("GRAFANA_TOKEN")

	if grafanaURL == "" && grafanaToken == "" {
		log.Println("WARN [Grafana probe] GRAFANA_URL and GRAFANA_TOKEN are not set — Grafana annotations will be skipped. On OpenShift, inject these via `oc set env deployment/hoplon GRAFANA_URL=... GRAFANA_TOKEN=...`")
		return
	}
	if grafanaURL == "" {
		log.Println("WARN [Grafana probe] GRAFANA_URL is not set — Grafana annotations will be skipped.")
		return
	}
	if grafanaToken == "" {
		log.Println("WARN [Grafana probe] GRAFANA_TOKEN is not set — Grafana annotations will be skipped.")
		return
	}

	// Mixed-content warning: an HTTP Grafana URL inside an HTTPS-served Safety Automation is
	// silently blocked by the browser — the iframe shows blank with no error shown.
	// This is the most common misconfiguration on OpenShift, where Routes are HTTPS
	// by default but Grafana is often configured with http:// in values.yaml.
	if strings.HasPrefix(grafanaURL, "http://") {
		log.Println("WARN [Grafana probe] GRAFANA_URL uses plain HTTP (http://). " +
			"If DynaCov Safety Automation itself gets served over HTTPS (https://), the browser " +
			"will silently block the embedded Grafana dashboard due to mixed content. " +
			"The dashboard panel then will appear blank with no error shown. " +
			"Change GRAFANA_URL to https:// if DynaCov Safety Automation itself is accessed via HTTPS.")
	}

	// Hit /api/org — lightweight authenticated endpoint that returns the current
	// organisation. A 200 confirms both network reachability and token validity.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, grafanaURL+"/api/org", nil)
	if err != nil {
		log.Printf("WARN [Grafana probe] Could not build probe request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+grafanaToken)

	grafanaResponse, err := grafana.GrafanaHTTPClient.Do(req)
	if err != nil {
		log.Printf("WARN [Grafana probe] Could not reach Grafana at %s: %v — check network connectivity from this pod to Grafana.", grafanaURL, err)
		probeGrafanaHTTPFallback(grafanaURL, grafanaToken)
		return
	}
	defer grafanaResponse.Body.Close()

	if grafanaResponse.StatusCode == http.StatusUnauthorized || grafanaResponse.StatusCode == http.StatusForbidden {
		// Auth failure — no point retrying with http://, the token is the problem.
		log.Printf("WARN [Grafana probe] Grafana at %s rejected the token (HTTP %d) — GRAFANA_TOKEN may be invalid or expired.", grafanaURL, grafanaResponse.StatusCode)
		return
	}
	if grafanaResponse.StatusCode < 200 || grafanaResponse.StatusCode >= 300 {
		log.Printf("WARN [Grafana probe] Grafana probe returned unexpected HTTP %d from %s.", grafanaResponse.StatusCode, grafanaURL)
		probeGrafanaHTTPFallback(grafanaURL, grafanaToken)
		return
	}

	// Decode the org response to auto-discover the org ID — eliminates the need for
	// the administrator to supply grafana.orgId in values.yaml.
	body, readErr := io.ReadAll(grafanaResponse.Body)

	if readErr != nil {
		log.Printf("WARN [Grafana probe] Grafana reachable at %s (HTTP %d) but could not read org response: %v — Grafana organisation ID for the dashboard cannot be set therefore and Grafana integration not work therefore.", grafanaURL, grafanaResponse.StatusCode, readErr)
		return
	}

	var orgResp struct {
		ID int `json:"id"`
	}

	if jsonErr := json.Unmarshal(body, &orgResp); jsonErr != nil || orgResp.ID == 0 {
		log.Printf("WARN [Grafana probe] Grafana reachable at %s (HTTP %d) but could not decode org ID: %v — Grafana organisation ID for the dashboard cannot be set therefore and Grafana integration not work therefore.", grafanaURL, grafanaResponse.StatusCode, jsonErr)
		return
	}

	grafana.OrgID = strconv.Itoa(orgResp.ID)

	log.Printf("Grafana reachable at %s, token accepted (HTTP %d), org ID auto-discovered: %s.", grafanaURL, grafanaResponse.StatusCode, grafana.OrgID)

	// Check 2 — verify the Safety Automation Resilience Dashboard has been imported into Grafana.
	// Only runs when check 1 succeeded (Grafana reachable, token valid, org ID known).
	// Admins often forget the import step — the iframe on run detail pages breaks silently
	// without this check, with no error visible until a user opens a run.
	dashCtx, dashCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dashCancel()

	dashReq, err := http.NewRequestWithContext(dashCtx, http.MethodGet,
		grafanaURL+"/api/dashboards/uid/"+grafana.DashboardUID, nil)
	if err != nil {
		log.Printf("WARN [Grafana probe] Could not build dashboard check request: %v", err)
		return
	}
	dashReq.Header.Set("Authorization", "Bearer "+grafanaToken)

	dashResp, err := grafana.GrafanaHTTPClient.Do(dashReq)
	if err != nil {
		log.Printf("WARN [Grafana probe] Dashboard check failed: %v", err)
		return
	}
	defer dashResp.Body.Close()

	if dashResp.StatusCode == http.StatusNotFound {
		log.Printf("WARN [Grafana probe] DynaCov Safety Automation dashboard (uid: %s) not found in Grafana — import chart/grafana/hoplon-resilience-dashboard.json into the Grafana Public organisation. The metrics iframe on run detail pages will be broken until this is done.", grafana.DashboardUID)
		return
	}
	if dashResp.StatusCode < 200 || dashResp.StatusCode >= 300 {
		log.Printf("WARN [Grafana probe] Dashboard check returned unexpected HTTP %d for uid %s.", dashResp.StatusCode, grafana.DashboardUID)
		return
	}

	log.Printf("DynaCov Safety Automation Grafana dashboard found and accessible.")

}

// probeGrafanaHTTPFallback is called when an https:// Grafana probe fails. It retries
// the same probe using http:// to detect the common misconfiguration where the Grafana
// OpenShift Route has no TLS configured. If HTTP succeeds, it logs a detailed explanation
// of both the immediate fix and the mixed-content risk.
func probeGrafanaHTTPFallback(originalURL, token string) {
	if !strings.HasPrefix(originalURL, "https://") {
		return
	}
	httpURL := "http://" + originalURL[len("https://"):]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/api/org", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := grafana.GrafanaHTTPClient.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return // HTTP also failed — not a TLS route issue, something else is wrong.
	}
	defer resp.Body.Close()

	// HTTP worked, HTTPS did not — the Grafana Route has no TLS termination.
	log.Printf("WARN [Grafana probe] Grafana is reachable via HTTP (%s) but NOT via HTTPS (%s). "+
		"The Grafana OpenShift Route does not have TLS configured — set GRAFANA_URL to %s for now. "+
		"WARNING: In that case however if DynaCov Safety Automation itself is then still served over HTTPS (https://), the browser will "+
		"silently block the Grafana iframe because browsers refuse to embed HTTP content inside HTTPS pages (mixed content). "+
		"Permanent fix: add TLS edge termination to the Grafana Route: "+
		"oc patch route <grafana-route> -n <grafana-namespace> --type=merge "+
		"-p '{\"spec\":{\"tls\":{\"termination\":\"edge\",\"insecureEdgeTerminationPolicy\":\"Redirect\"}}}'",
		httpURL, originalURL, httpURL)
}

// dropDB is a helper used during development to reset the schema.
func dropDB(db *sql.DB) {
	tables := []string{"TEST_RUN_LOG", "TEST_RUN_URLS_FOR_WORKLOAD", "TEST_RUN_FINDING", "TEST_RUN_FINDING_TYPE", "TEST_RUN_VERDICT", "TEST_RUN_PHASE", "TEST_RUN", "TEST_SCENARIO_TYPE", "APPLICATION_USER"}
	for _, t := range tables {
		db.Exec(`BEGIN EXECUTE IMMEDIATE 'DROP TABLE ` + t + ` CASCADE CONSTRAINTS'; EXCEPTION WHEN OTHERS THEN NULL; END;`)
	}
}

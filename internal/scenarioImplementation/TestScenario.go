package scenarioImplementation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/grafana"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/logger"
)

// ── K6 job naming ─────────────────────────────────────────────────────────────
//
// All K6 jobs created by Safety Automation follow the pattern:
//   hoplon-k6-<scenario>-<runType>-<runId>
//
// The pattern serves three purposes:
//  1. The Grafana dashboard uses source_workload=~"hoplon-k6-.*" to identify
//     K6 client traffic in all panels — a single regex covers every scenario.
//  2. The scenario and run type are immediately readable in oc get jobs output
//     without needing to look up the run ID in the database.
//  3. The ConfigMap for each job reuses the same name (different resource type,
//     no naming conflict) so the pairing is always unambiguous.
//
// Scenario constants (K6ScenarioXxx) and run-type constants (K6RunTypeXxx)
// are the only authoritative values — never hardcode the strings elsewhere.

// Scenario identifiers — abbreviated to stay readable in Prometheus labels.
const (
	K6ScenarioPodKill      = "pod-kill"
	K6ScenarioHttpDepDelay = "http-dep-delay"
	K6ScenarioHttpDepError = "http-dep-error"
	K6ScenarioResStress    = "res-stress"
)

// Phase label constants for TEST_RUN_PHASE. Free strings — no type table.
// The unique index on (TEST_RUN_ID, PHASE_LABEL) enforces uniqueness per run.
// Constants here prevent typos at compile time.
// todo move individual scenarios
const (
	PhaseBaseline        = "baseline"
	PhaseChaosKillLoad   = "chaos-kill-under-load"
	PhaseChaosKillNoLoad = "chaos-kill-no-load"
	PhaseChaosHttpFault  = "chaos-http-fault"
)

// Run-type identifiers — what the K6 job is doing within its scenario.
const (
	K6RunTypeLoad = "load" // main load test (all scenarios)
	K6RunTypeDisc = "disc" // dependency discovery (HttpDependencyDelayScenario only)
)

// TestScenario is the interface that every Resilience TestScenario Test must implement.
type TestScenario interface {
	Run(
		ctx context.Context,
		testRunId int64,
		target applicationModelStructs.Workload,
		urls []database.TestRunURL,
		kubernetesClient *restClient.KubernetesClient,
		db *sql.DB,
		databaseSSELogger *logger.DatabaseSSELogger,
	) error

	// CreateFinalVerdicts is called after Run() completes, regardless of outcome.
	// May read previously written verdicts via database.GetTestRunVerdictsOrderedByCreationTime() and
	// phase windows via database.GetTestRunPhases(). May write additional verdicts
	// via database.WriteVerdict() followed by log.Info() for SSE visibility.
	// Must return nil when no verdict can be computed — a missing verdict must never
	// fail the run record. Called with a 30-second timeout. No kubernetesClient —
	// verdicts only touch Prometheus and the DB.
	CreateFinalVerdicts(
		ctx context.Context,
		testRunId int64,
		target applicationModelStructs.Workload,
		db *sql.DB,
		sseLogger *logger.DatabaseSSELogger,
	) error
}

// Registry maps GoStructName → a factory function that returns a fresh TestScenario
// instance for each test run. Using factories (not singleton instances) is critical:
// then when scenario structs carry per-run state, so sharing one instance across
// concurrent runs would be a data race otherwise. All current scenario structs are
// empty, so there is no behavioral change today — but the registry shape is correct
// for when fields are added.
var Registry = map[string]func() TestScenario{
	"PodKillAndStartScenario":     func() TestScenario { return &PodKillAndStartScenario{} },
	"HttpDependencyDelayScenario": func() TestScenario { return &HttpDependencyDelayScenario{} },
	"HttpDependencyErrorScenario": func() TestScenario { return &HttpDependencyErrorScenario{} },
	"ResourceStressScenario":      func() TestScenario { return &ResourceStressScenario{} },
}

// GetScenario looks up a registered scenario by its Go struct name and returns a
// fresh instance. Each call returns a new struct so concurrent runs of the same
// scenario do not share state.
func GetScenario(goStructName string) (TestScenario, error) {
	factory, ok := Registry[goStructName]
	if !ok {
		return nil, fmt.Errorf("scenario %q not found in registry", goStructName)
	}
	return factory(), nil
}

// FormatURLsForLog returns a comma-separated string of the configured target URLs,
// each prefixed with its HTTP method and suffixed with "[with payload]" when a
// request body is present. Used in the startup log line of every scenario.
//
// Example output:
//
//	GET http://example.com/health, POST http://example.com/checkout [with payload]
func FormatURLsForLog(urls []database.TestRunURL) string {
	var b strings.Builder
	for i, u := range urls {
		b.WriteString(strings.ToUpper(u.HTTPMethod))
		b.WriteString(" ")
		b.WriteString(u.URLString)
		if u.RequestBody != "" {
			b.WriteString(" [with payload]")
		}
		if i < len(urls)-1 {
			b.WriteString(", ")
		}
	}
	return b.String()
}

// K6JobName returns the canonical Safety Automation K6 job name for the given scenario,
// run type, and test run ID. Use the same value for the accompanying ConfigMap.
func K6JobName(scenario, runType string, runId int64) string {
	return fmt.Sprintf("hoplon-k6-%s-%s-%d", scenario, runType, runId)
}

func K6ScriptNameFromK6JobName(k6JobName string) string {
	return fmt.Sprintf("scdript-%s", k6JobName)
}

// addAnnotationToChart posts a Grafana annotation to the Safety Automation Resilience
// Dashboard. Tags are built from the run ID, the caller-supplied action tag,
// and the workload namespace/name so annotations are filterable per run and
// per workload. Failures are logged as warnings — an annotation miss must never
// abort a test run.
func addAnnotationToChart(
	ctx context.Context,
	annotationTextToShow string,
	testRunId int64,
	specificActionTagAsString string,
	target applicationModelStructs.Workload,
	databaseSSELogger *logger.DatabaseSSELogger,
) error {

	tags := []string{
		//Create central
		//RunId Formatter
		fmt.Sprintf("Safety Automation RUN-%06d", testRunId),
		specificActionTagAsString,
		"namespace:" + target.Namespace,
		"workload:" + target.Name,
	}
	err := grafana.AddAnnotation(ctx, annotationTextToShow, tags, "istio-workload-overview-version-two")
	if err != nil {
		databaseSSELogger.Warn("Could not post Grafana annotation for tags %v: %s", tags, err.Error())
	}
	return err
}

// GenerateK6Script builds a K6 JavaScript test script from the configured URLs.
// All scenarios use the same script shape: 2 VUs, 10-minute safety-net ceiling
// (the job is always deleted early by the scenario), one request per URL per
// iteration, non-200 responses logged to console, 1-second sleep between loops.
//
// The 10-minute duration is intentionally generous — the scenario controls the
// actual runtime via DeleteK6JobAndConfigMap, not the K6 duration flag.
func GenerateK6Script(urls []database.TestRunURL) string {
	var sb strings.Builder
	sb.WriteString("import http from \"k6/http\";\nimport { sleep } from \"k6\";\n\n")
	sb.WriteString("// Duration is a safety-net ceiling — the job is cleaned up early by the scenario.\n")
	sb.WriteString("export const options = { vus: 2, duration: \"10m\" };\n\n")
	sb.WriteString("export default function () {\n")
	for i, u := range urls {
		method := strings.ToUpper(u.HTTPMethod)
		if u.RequestBody != "" {
			// Each URL gets a uniquely named variable (r0, r1, …) to avoid JS scope conflicts.
			if u.ContentType != "" {
				sb.WriteString(fmt.Sprintf("  const r%d = http.request(%q, %q, %q, { headers: { 'Content-Type': %q } });\n",
					i, method, u.URLString, u.RequestBody, u.ContentType))
			} else {
				// No Content-Type configured — send the body without a Content-Type header.
				// The user left the field blank intentionally; we do not inject a default.
				sb.WriteString(fmt.Sprintf("  const r%d = http.request(%q, %q, %q);\n",
					i, method, u.URLString, u.RequestBody))
			}
		} else {
			sb.WriteString(fmt.Sprintf("  const r%d = http.request(%q, %q, null);\n", i, method, u.URLString))
		}
		// Log every non-200 response so the status code is visible in the Safety Automation run log.
		// This is the primary diagnostic for understanding why http_req_failed > 0.
		sb.WriteString(fmt.Sprintf("  if (r%d.status !== 200) { console.log('%s %s → HTTP ' + r%d.status); }\n",
			i, method, u.URLString, i))
	}
	sb.WriteString("  sleep(1);\n}\n")
	return sb.String()
}

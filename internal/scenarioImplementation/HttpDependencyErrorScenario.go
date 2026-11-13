package scenarioImplementation

/*
File @internal/scenarioImplementation/HttpDependencyErrorScenario.go implements the
HttpDependencyErrorScenario. It injects HTTP 503 error responses on 50% of requests
to the outbound dependency edges of the workload under test using Istio VirtualService
fault injection and measures the impact via K6 load generation.

Shared runner: httpDependencyScenario.go — all lifecycle and VS logic lives there.
This file only declares the struct and constructs the abort-specific fault block.
*/

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/logger"
)

// Fault parameters for the error scenario.
// 503 at 50% is deliberately partial — it triggers circuit breakers and exposes
// missing fallback logic while keeping enough successful requests in-flight to
// make the Grafana error-rate graph clearly readable (spike vs. complete blackout).
const (
	hddeErrorHttpStatus = 503
	hddeErrorPercentage = 50
)

// HttpDependencyErrorScenario injects HTTP 503 error responses on 50% of requests
// to the outbound dependency edges of the target workload using Istio VirtualService
// fault injection and measures the impact via K6 load generation.
//
// Complements HttpDependencyDelayScenario: where the delay scenario tests timeout and
// retry-under-latency behaviour, this scenario tests circuit breaker triggering,
// error budget consumption, and fallback logic when a dependency returns hard errors.
//
// See httpDependencyScenario.go for the shared runner.
type HttpDependencyErrorScenario struct{}

func (s *HttpDependencyErrorScenario) Run(ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	kubernetesClient *restClient.KubernetesClient,
	db *sql.DB,
	databaseSSELogger *logger.DatabaseSSELogger) error {

	return runHttpDependencyScenario(ctx, HttpDependencyFaultConfig{
		DisplayName:      "HTTP Dependency Error",
		FaultDescription: fmt.Sprintf("HTTP %d on %d%% of requests", hddeErrorHttpStatus, hddeErrorPercentage),
		K6ScenarioKey:    K6ScenarioHttpDepError,
		FaultBlock: map[string]interface{}{
			"abort": map[string]interface{}{
				// httpStatus must be int64 — Istio's protobuf schema uses int32,
				// and the Kubernetes unstructured encoder maps JSON numbers to int64.
				"httpStatus": int64(hddeErrorHttpStatus),
				"percentage": map[string]interface{}{
					"value": float64(hddeErrorPercentage),
				},
			},
		},
	}, testRunId, target, urls, kubernetesClient, db, databaseSSELogger)
}

func (s *HttpDependencyErrorScenario) CreateFinalVerdicts(
	ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	db *sql.DB,
	log *logger.DatabaseSSELogger,
) error {
	log.Info("Verdict computation not yet implemented for HttpDependencyErrorScenario.")
	return nil
}

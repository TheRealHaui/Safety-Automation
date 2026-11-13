package scenarioImplementation

/*
File @internal/scenarioImplementation/HttpDependencyDelayScenario.go implements the
HttpDependencyDelayScenario. It injects Istio VirtualService latency faults on the
ingoing downstream dependencies of the workload under test and measures the impact via
K6 load generation and the Safety Automation Resilience Grafana Dashboard.

Design blueprint: PodKillAndStartScenario.go — same structure, same patterns,
same Grafana annotation style, same Prometheus scrape-interval-aware warmup.
Implementation plan: resources-various/plans-worked-through-with-claude/NetworkLatencnyScenario-implementation.md

Shared runner: httpDependencyScenario.go — all lifecycle and VS logic lives there.
This file only declares the struct and constructs the delay-specific fault block.
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

// Fault parameters for the delay scenario.
// 2s delay at 100% exposes missing timeouts, tight timeouts, retry-without-backoff,
// and missing circuit breakers without being so long that it just looks like a dead
// service. See the implementation plan for the full reasoning table.
const (
	hddsDelaySeconds    = 2
	hddsDelayPercentage = 100
)

// HttpDependencyDelayScenario injects artificial network latency at the dependency
// edges of the target workload using Istio VirtualService fault injection and
// measures the impact via K6 load generation.
//
// The scenario auto-discovers dependencies by querying Prometheus for outbound
// istio_requests_total metrics while K6 drives traffic — no manual dependency
// configuration needed.
//
// See httpDependencyScenario.go for the shared runner.
type HttpDependencyDelayScenario struct{}

func (s *HttpDependencyDelayScenario) Run(ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	kubernetesClient *restClient.KubernetesClient,
	db *sql.DB,
	databaseSSELogger *logger.DatabaseSSELogger) error {

	return runHttpDependencyScenario(ctx, HttpDependencyFaultConfig{
		DisplayName:      "HTTP Dependency Delay",
		FaultDescription: fmt.Sprintf("%ds latency on %d%% of requests", hddsDelaySeconds, hddsDelayPercentage),
		K6ScenarioKey:    K6ScenarioHttpDepDelay,
		FaultBlock: map[string]interface{}{
			"delay": map[string]interface{}{
				// fixedDelay is a Duration string recognised by Istio/Envoy (e.g. "2s").
				"fixedDelay": fmt.Sprintf("%ds", hddsDelaySeconds),
				"percentage": map[string]interface{}{
					"value": float64(hddsDelayPercentage),
				},
			},
		},
	}, testRunId, target, urls, kubernetesClient, db, databaseSSELogger)
}

func (s *HttpDependencyDelayScenario) CreateFinalVerdicts(
	ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	db *sql.DB,
	sseLogger *logger.DatabaseSSELogger,
) error {
	sseLogger.Info("Verdict computation not yet implemented for HttpDependencyDelayScenario.")
	return nil
}

package scenarioImplementation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/logger"
)

// ResourceStressScenario applies CPU and memory stress to the target workload
// and measures its resilience under resource pressure.
// TODO: Implement using a stress-ng job deployed into the namespace.
// This scenario is commented out for the user interface.
type ResourceStressScenario struct{}

func (s *ResourceStressScenario) Run(ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	kubernetesClient *restClient.KubernetesClient,
	db *sql.DB,
	databaseSSELogger *logger.DatabaseSSELogger) error {

	databaseSSELogger.Info("ResourceStressScenario starting for workload %s/%s", target.Namespace, target.Name)

	// TODO: Step 1 – Start K6 baseline load
	// TODO: Step 2 – Deploy stress-ng job in same namespace
	// TODO: Step 3 – Ramp CPU stress to 80% over 30s, hold for 60s
	// TODO: Step 4 – Add memory pressure, hold for 30s
	// TODO: Step 5 – Remove stress job
	// TODO: Step 6 – Compare before/after K6 metrics

	databaseSSELogger.Warn("ResourceStressScenario is not yet implemented – running stub for %s", target.Name)
	time.Sleep(3 * time.Second)
	databaseSSELogger.Info("ResourceStressScenario stub completed")

	return errors.New("ResourceStressScenario is not yet implemented")

}

func (s *ResourceStressScenario) CreateFinalVerdicts(
	ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	db *sql.DB,
	sseLogger *logger.DatabaseSSELogger,
) error {
	sseLogger.Info("Verdict computation not yet implemented for ResourceStressScenario.")
	return nil
}

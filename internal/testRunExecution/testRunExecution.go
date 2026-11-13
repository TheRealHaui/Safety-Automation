package testRunExecution

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/logger"
	"github.com/TheRealHaui/Safety-Automation/internal/scenarioImplementation"
)

// activeRun holds everything needed to manage a running test.
type activeRun struct {
	cancel    context.CancelFunc
	logStream chan database.TestRunLog
	sseSubs   map[chan database.TestRunLog]struct{}
	subsMu    sync.Mutex
}

// TestRunScenarioExecutionManager manages test run execution via a bounded worker pool.
type TestRunScenarioExecutionManager struct {
	mainDB           *sql.DB
	logDB            *sql.DB // separate connection for autonomous log writes
	kubernetesClient *restClient.KubernetesClient
	k6Timeout        time.Duration

	queue  chan *jobRequest
	mu     sync.Mutex
	active map[int64]*activeRun
}

type jobRequest struct {
	testRunID int64
	workload  applicationModelStructs.Workload
	scenario  scenarioImplementation.TestScenario
	urls      []database.TestRunURL
	userID    int64
}

// New creates a TestRunScenarioExecutionManager and starts the worker pool.
func New(mainDB, logDB *sql.DB, kubernetesClient *restClient.KubernetesClient, maxWorkers int, k6Timeout time.Duration) *TestRunScenarioExecutionManager {
	m := &TestRunScenarioExecutionManager{
		mainDB:           mainDB,
		logDB:            logDB,
		kubernetesClient: kubernetesClient,
		k6Timeout:        k6Timeout,
		queue:            make(chan *jobRequest, 256),
		active:           make(map[int64]*activeRun),
	}
	for i := 0; i < maxWorkers; i++ {
		go m.worker()
	}
	return m
}

// Enqueue adds a test run to the execution queue and returns immediately.
func (executionManager *TestRunScenarioExecutionManager) Enqueue(testRunID int64, workload applicationModelStructs.Workload, scen scenarioImplementation.TestScenario, urls []database.TestRunURL) error {
	executionManager.mu.Lock()
	defer executionManager.mu.Unlock()

	logStream := make(chan database.TestRunLog, 512)
	ctx, cancel := context.WithTimeout(context.Background(), executionManager.k6Timeout+5*time.Minute)

	executionManager.active[testRunID] = &activeRun{
		cancel:    cancel,
		logStream: logStream,
		sseSubs:   make(map[chan database.TestRunLog]struct{}),
	}

	select {
	case executionManager.queue <- &jobRequest{testRunID: testRunID, workload: workload, scenario: scen, urls: urls}:
	default:
		cancel()
		delete(executionManager.active, testRunID)
		return fmt.Errorf("worker queue is full, try again later")
	}

	// Start a goroutine to fan out log stream to all SSE subscribers
	go executionManager.fanOut(ctx, testRunID, logStream)

	return nil
}

// Cancel cancels an active test run.
func (executionManager *TestRunScenarioExecutionManager) Cancel(testRunID int64) error {
	executionManager.mu.Lock()
	run, ok := executionManager.active[testRunID]
	executionManager.mu.Unlock()
	if !ok {
		return fmt.Errorf("no active test run %d", testRunID)
	}
	run.cancel()
	return nil
}

// Subscribe returns a channel that receives log entries for the given test run.
// The caller must call Unsubscribe when done.
func (executionManager *TestRunScenarioExecutionManager) Subscribe(testRunID int64) (chan database.TestRunLog, bool) {
	executionManager.mu.Lock()
	run, ok := executionManager.active[testRunID]
	executionManager.mu.Unlock()
	if !ok {
		return nil, false
	}
	ch := make(chan database.TestRunLog, 128)
	run.subsMu.Lock()
	run.sseSubs[ch] = struct{}{}
	run.subsMu.Unlock()
	return ch, true
}

// Unsubscribe removes a subscriber channel.
func (executionManager *TestRunScenarioExecutionManager) Unsubscribe(testRunID int64, ch chan database.TestRunLog) {
	executionManager.mu.Lock()
	run, ok := executionManager.active[testRunID]
	executionManager.mu.Unlock()
	if !ok {
		return
	}
	run.subsMu.Lock()
	delete(run.sseSubs, ch)
	run.subsMu.Unlock()
}

// IsActive reports whether a test run is currently active.
func (executionManager *TestRunScenarioExecutionManager) IsActive(testRunID int64) bool {
	executionManager.mu.Lock()
	defer executionManager.mu.Unlock()
	_, ok := executionManager.active[testRunID]
	return ok
}

// worker picks jobs off the queue and executes them.
func (executionManager *TestRunScenarioExecutionManager) worker() {
	for req := range executionManager.queue {
		executionManager.execute(req)
	}
}

func (executionManager *TestRunScenarioExecutionManager) execute(req *jobRequest) {
	executionManager.mu.Lock()
	run, ok := executionManager.active[req.testRunID]
	executionManager.mu.Unlock()
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), executionManager.k6Timeout+5*time.Minute)
	defer cancel()

	// Replace the cancel in the active run with one that cancels this ctx too
	combinedCancel := func() {
		cancel()
		run.cancel()
	}
	executionManager.mu.Lock()
	executionManager.active[req.testRunID].cancel = combinedCancel
	executionManager.mu.Unlock()

	log := logger.New(req.testRunID, executionManager.logDB, run.logStream)

	// Mark RUNNING
	if err := database.UpdateTestRunStatus(ctx, executionManager.mainDB, req.testRunID, applicationModelStructs.StatusRunning, ""); err != nil {
		log.Error("Failed to update status to RUNNING: %v", err)
	}

	log.Info("Test run number %d created for workload %s/%s", req.testRunID, req.workload.Namespace, req.workload.Name)

	// Execute the scenario
	err := req.scenario.Run(ctx, req.testRunID, req.workload, req.urls, executionManager.kubernetesClient, executionManager.mainDB, log)

	var finalStatus applicationModelStructs.TestRunStatusTypeStruct
	var summary string

	switch {
	case ctx.Err() == context.Canceled:
		finalStatus = applicationModelStructs.StatusCancelled
		summary = "Test run was cancelled by user."
		log.Warn("Test run %d was cancelled", req.testRunID)
	case err != nil:
		finalStatus = applicationModelStructs.StatusFailed
		summary = fmt.Sprintf("Test run failed: %v", err)
		log.Error("Test run %d failed: %v", req.testRunID, err)
	default:
		finalStatus = applicationModelStructs.StatusSuccess
		summary = "Test run completed successfully."
		log.Info("Test run %d completed successfully", req.testRunID)
	}

	if err := database.UpdateTestRunStatus(context.Background(), executionManager.mainDB, req.testRunID, finalStatus, summary); err != nil {
		log.Error("Failed to update final status: %v", err)
	}

	// CreateFinalVerdicts runs after every test, regardless of outcome. A failed run
	// may have recorded intermediate verdicts worth preserving. A dedicated timeout
	// prevents a slow or unreachable Prometheus from blocking the worker goroutine.
	// The run ctx is not used here — it may already be cancelled by the time Run() returns.
	verdictCtx, verdictCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer verdictCancel()
	if err := req.scenario.CreateFinalVerdicts(
		verdictCtx, req.testRunID, req.workload, executionManager.mainDB, log,
	); err != nil {
		log.Warn("Verdict computation failed for run %d: %v", req.testRunID, err)
	}

	// Clean up active run after a brief delay to allow SSE clients to drain
	time.AfterFunc(5*time.Second, func() {
		executionManager.mu.Lock()
		delete(executionManager.active, req.testRunID)
		executionManager.mu.Unlock()
		close(run.logStream)
	})
}

// fanOut reads from logStream and broadcasts to all SSE subscribers.
func (executionManager *TestRunScenarioExecutionManager) fanOut(ctx context.Context, testRunID int64, logStream chan database.TestRunLog) {
	for entry := range logStream {
		executionManager.mu.Lock()
		run, ok := executionManager.active[testRunID]
		executionManager.mu.Unlock()
		if !ok {
			break
		}
		run.subsMu.Lock()
		for ch := range run.sseSubs {
			select {
			case ch <- entry:
			default:
			}
		}
		run.subsMu.Unlock()
	}
}

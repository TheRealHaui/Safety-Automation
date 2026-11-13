package scenarioImplementation

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	prometheusClient "github.com/TheRealHaui/Safety-Automation/internal/backend/prometheus"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/logger"
)

//For Claude to communicate with it:
/*
File @internal/scenarioImplementation/PodKillAndStartScenario.go is exactly there to do the OpenShift workload/pod kill scenario tests. The Istio golden signals get monitored while a k6 test
  uses the Microservice/the workload/the pods. So, the idea is to start more than one pods, delete one and so on. If http 5x states get recorded we know there is some problem. This is the
  first most simple test scenario run.
*/

// PodKillAndStartScenario simulates pod failure and starts by deleting one or more pods from
// the target workload while K6 applies continuous load via the provided URLs.
type PodKillAndStartScenario struct{}

func (s *PodKillAndStartScenario) Run(ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	kubernetesClient *restClient.KubernetesClient,
	db *sql.DB,
	databaseSSELogger *logger.DatabaseSSELogger) error {

	databaseSSELogger.Info("PodKillAndStartScenario initialized for workload %s/%s with %d configured target URLs.", target.Namespace, target.Name, len(urls))

	// "Action: Test run started" and "Action: Test run ended" are matched by name in the
	// Safety Automation Dashboard annotation definitions (Grafana Public org, dashboard UID:
	// istio-workload-overview-version-two). Each tag maps to a colour:
	//   "Action: Test run started" → green  (rgba(50, 172, 45, 1))
	//   "Action: Test run ended"   → red    (rgba(245, 54, 54, 1))
	// These colour mappings are configured in the dashboard JSON itself — changing the
	// tag value here requires updating the dashboard annotation filter in Grafana too.
	// Further, note that Grafana removes the space after ":". Further, only when there
	// is a ":". Other spaces get kept. The Dashboard annotation was adapted accordingly.
	// Further, therefore, we remove the spaces here too in the source code. Do not
	// change that unless verifying the colors of the annotation in the Dashboard
	// that gets shown to the end user too.
	// According to Claude this is the only option for annotations in Grafana to
	// include custom styling. That has to be verified later.
	// NOTE: The colour definitions are correctly configured in the dashboard JSON and
	// the tags match — but as of Grafana 12.4.2 the colours are not rendering in the
	// dashboard. Root cause unknown; suspected Grafana 12.x rendering quirk. Parked as
	// extremely low priority/impact.
	specificActionTagAsString := "Action:Test Scenario Run \"Pod Kill And Start\" started"

	scaleToReplicasAtTheEndAgain := target.ReadyReplicas

	allUrlsAsCommaSeparatedList := FormatURLsForLog(urls)

	databaseSSELogger.Info("PodKillAndStartScenario starting for workload %s/%s which currently has %d replicas running",
		target.Namespace, target.Name, scaleToReplicasAtTheEndAgain)

	if len(urls) == 1 {
		databaseSSELogger.Info("The following target URL was configured: %s.", allUrlsAsCommaSeparatedList)
	} else {
		databaseSSELogger.Info("The following %d target URLs were configured: %s.", len(urls), allUrlsAsCommaSeparatedList)
	}

	//Grafana Dashboard
	//Test Start
	_ = addAnnotationToChart(ctx, fmt.Sprintf("Test Scenario Run \"Pod Kill And Start\" RUN-%06d started", testRunId),
		testRunId, specificActionTagAsString, target, databaseSSELogger)

	//We have to wait
	//that time because otherwise
	//the Grafana Dashboard does
	//show annotations too close
	time.Sleep(5 * time.Second)

	defer func() {

		//We have to wait
		//that time because otherwise
		//the Grafana Dashboard does
		//show annotations too close
		time.Sleep(5 * time.Second)

		//Grafana Dashboard
		//Test end — annotated after both pod kills and the K6 run are complete.
		_ = addAnnotationToChart(ctx, fmt.Sprintf("Test Scenario Run \"Pod Kill And Start\" RUN-%06d ended", testRunId),
			testRunId, "Action:Test Scenario Run \"Pod Kill And Start\" ended", target, databaseSSELogger)

	}()

	//Always scale
	//back important
	defer scaleToInitialNumberOfPods(kubernetesClient, target, scaleToReplicasAtTheEndAgain, databaseSSELogger)

	// Read the Prometheus scrape interval live so warm-up durations are grounded
	// in the actual cluster config rather than a hardcoded guess.
	// Requires GRAFANA_PROMETHEUS_DATASOURCE_UID in the environment — see .env.
	// Falls back to 15s if unset or if Grafana is unreachable.
	//
	// Why we need warm-up waits at all — the Prometheus counter-reset problem:
	// Istio sidecar metrics (istio_requests_total, etc.) are in-memory counters
	// inside the Envoy process. When a pod is killed the process dies and the
	// counter is gone. The replacement pod starts all counters at zero. Prometheus
	// handles counter resets in rate()/increase() by detecting the drop — but ONLY
	// if it managed to scrape the counter while it was non-zero before the kill.
	// If Prometheus never scraped in the window between "K6 started sending" and
	// "pod was killed", those requests are permanently invisible to the dashboard.
	prometheusInterval := prometheusClient.FetchKubernetesPodsScrapeInterval(ctx)

	// Why 2 × interval + 2s?
	// rate() and irate() need at least two consecutive scrape data points to produce
	// a value — one data point is not enough to compute a derivative. Worst case:
	// K6 starts 1 second after a scrape fires, so one full interval must elapse
	// before the next scrape lands. A second interval guarantees the second sample.
	// The +2s absorbs scrape jitter and network delay.
	// Example with the current cluster config: 15s interval → warmup = 32s.
	warmupDuration := 2*prometheusInterval + 2*time.Second

	databaseSSELogger.Info("Prometheus scrape interval is %g seconds; waiting %g seconds before each significant action (2 × interval + 2s) to ensure "+
		"Grafana dashboard panels capture the load.", prometheusInterval.Seconds(), warmupDuration.Seconds())

	_, err2 := doPodKillRestartAutomaticallyReturnAndSaveToDatabaseBaseRecoveryTimeWithoutLoadTestOne(ctx, s, testRunId, target,
		databaseSSELogger, kubernetesClient, db)
	if err2 != nil {
		return err2
	}

	databaseSSELogger.Info("Going to wait %g seconds again before executing next test to ensure Prometheus is synchronized.", warmupDuration.Seconds())
	time.Sleep(warmupDuration)

	if err := doPodKillRestartAutomaticallyReturnAndSaveToDatabaseBaseRecoveryTimeWithLoadTestTwo(ctx, testRunId, target, urls,
		kubernetesClient, db, databaseSSELogger, s, 5*time.Minute, warmupDuration); err != nil {

		return err

	}

	// Both findings are now written — compute the load-impact sub-verdict.
	if err := s.createRecoveryLoadImpactVerdict(ctx, testRunId, db, databaseSSELogger); err != nil {
		databaseSSELogger.Error("Could not write recovery load impact verdict for run %d: %v", testRunId, err)
		log.Printf("PodKillAndStartScenario: recovery load impact verdict failed for run %d: %v", testRunId, err)
		return err
	}

	return nil

}

// ── Milestone 3: K6 load test and second pod kill ───────────────────────────
//
// The first kill above measured recovery WITHOUT load (baseline).
// Now we start a K6 job and kill a second pod to measure recovery UNDER load.
// K6 is started AFTER the baseline measurement to keep the two phases cleanly
// separated. The job is cleaned up via defer regardless of outcome.
//
// K6 script duration is intentionally set to a long safety-net value ("10m").
// The job is deleted early by DeleteK6JobAndConfigMap once recovery is measured.
func doPodKillRestartAutomaticallyReturnAndSaveToDatabaseBaseRecoveryTimeWithLoadTestTwo(ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	kubernetesClient *restClient.KubernetesClient,
	db *sql.DB,
	databaseSSELogger *logger.DatabaseSSELogger,
	s *PodKillAndStartScenario,
	recoveryTimeout time.Duration,
	warmupDuration time.Duration) error {

	k6JobName := K6JobName(K6ScenarioPodKill, K6RunTypeLoad, testRunId)
	k6ConfigMapName := K6ScriptNameFromK6JobName(k6JobName)

	databaseSSELogger.Info("Going execute entire test to delete a running Pod of workload %s WITH system load now.", target.Name)

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Test kill pod with load of Test Scenario Run \"Pod Kill And Start\" RUN-%06d started", testRunId),
		testRunId, "Action:Test kill pod with load start", target, databaseSSELogger)

	defer func() {
		databaseSSELogger.Info("Entire test to delete a running Pod of workload %s WITH system load finished.", target.Name)
		_ = addAnnotationToChart(ctx,
			fmt.Sprintf("Test kill pod with load of Test Scenario Run \"Pod Kill And Start\" RUN-%06d ended", testRunId),
			testRunId, "Action:Test kill pod with load end", target, databaseSSELogger)
	}()

	databaseSSELogger.Info("Starting K6 load job %s to generate traffic during second pod kill.", k6JobName)

	// K6_IMAGE is read from the environment here rather than being passed as a
	// parameter through the entire call chain (config → New() → Run() → here).
	// That chain is pure noise: the image is a deployment constant chosen by the
	// Safety Automation vendor, not a per-test-run concern. Reading it at the point of use
	// is the same pattern already used for GRAFANA_URL, GRAFANA_TOKEN, and
	// GRAFANA_PROMETHEUS_DATASOURCE_UID throughout this codebase.
	// The fallback to "grafana/k6:latest" is essential — without it an unset var
	// would silently produce an empty image name and the Kubernetes Job would fail.
	k6ImageToUse := os.Getenv("K6_IMAGE")
	if k6ImageToUse == "" {
		k6ImageToUse = "grafana/k6:latest"
	}

	k6Opts := restClient.K6JobOptions{
		TestRunID:     testRunId,
		Namespace:     target.Namespace,
		K6Image:       k6ImageToUse,
		Script:        GenerateK6Script(urls),
		ConfigMapName: k6ConfigMapName,
		JobName:       k6JobName,
	}

	if err := kubernetesClient.StartK6Job(ctx, k6Opts); err != nil {
		databaseSSELogger.Error("Could not start K6 job %s: %s", k6JobName, err.Error())
		log.Printf("PodKillAndStartScenario execution: could not start k6 job for run %d: %v", testRunId, err)
		return err
	}

	// K6 started — ensure cleanup regardless of what happens next.
	defer func() {

		kubernetesClient.DeleteK6JobAndConfigMap(context.Background(), k6JobName, k6ConfigMapName, target.Namespace)

		_ = addAnnotationToChart(ctx,
			fmt.Sprintf("K6 Load test for test kill pod with load of Test Scenario Run \"Pod Kill And Start\" RUN-%06d finished", testRunId),
			testRunId, "Action:K6 Load Test end", target, databaseSSELogger)

		//Make sure
		//annotation visible
		time.Sleep(5 * time.Second)

	}()

	// Wait until the K6 pod is actually Running before killing the second pod.
	// A fixed sleep is not reliable here — image pulls can take 30+ seconds on a
	// cold node, meaning we could kill the pod before K6 has sent a single request.
	// WaitForK6JobRunning polls the pod phase via the Kubernetes API (backed by etcd)
	// and returns only when phase == Running, i.e. K6 is generating load.
	databaseSSELogger.Info("Waiting for K6 job pod to reach Running phase before proceeding with second pod kill.")

	if err := kubernetesClient.WaitForK6JobRunning(ctx, k6JobName, target.Namespace); err != nil {
		databaseSSELogger.Error("K6 job pod did not reach Running phase: %s", err.Error())
		log.Printf("PodKillAndStartScenario execution: k6 pod never reached Running for run %d: %v", testRunId, err)
		return err
	}

	//If we reach that
	//here K6 job runs
	//Turned around
	//if statement

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("K6 Load test for test kill pod with load of Test Scenario Run \"Pod Kill And Start\" RUN-%06d started", testRunId),
		testRunId, "Action:K6 Load Test start", target, databaseSSELogger)

	databaseSSELogger.Info("K6 job pod is Running.")

	// K6 log streaming — waiting for a dedicated second tab in the run detail UI.
	// Streaming into the main execution log would mix K6 output with scenario log
	// entries. Once the second tab exists, uncomment the goroutine below and wire
	// it to a separate SSE endpoint for K6 logs.
	// See https://trello.com/c/WdKyM6v5 for design, screenshot, and full context.
	//Search for https://trello.com/c/WdKyM6v5 too to uncomment code that gets
	//used by this code
	/*
		go func() {
			if err := kubernetesClient.StreamK6JobLogs(ctx, k6JobName, target.Namespace,
				&sseLogWriter{log: databaseSSELogger, prefix: "K6"}); err != nil {
				// Expected at ctx cancellation or job completion — not an error worth surfacing.
				log.Printf("PodKillAndStartScenario: K6 log streaming ended for run %d: %v", testRunId, err)
			}
		}()
	*/

	//Spike has to be visible in the Dashboard
	databaseSSELogger.Info("Going to let K6 warm up for %g seconds before pod kill (ensures Prometheus scrapes K6 traffic at least twice before the spike).", warmupDuration.Seconds())
	time.Sleep(warmupDuration)

	databaseSSELogger.Info("Going to delete a running Pod of workload %s under K6 load now.", target.Name)

	killedPodName2, killTime2, err := kubernetesClient.DeleteOnePodUnGracefully(ctx, target)
	if err != nil {
		databaseSSELogger.Error("Could not delete second pod for workload %s/%s: %s", target.Namespace, target.Name, err.Error())
		log.Printf("PodKillAndStartScenario execution: could not delete second pod for %s/%s: %v", target.Namespace, target.Name, err)
		return err
	}

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Pod %s deleted under K6 load. Waiting for replacement to become ready.", killedPodName2),
		testRunId, fmt.Sprintf("Pod %s deleted under K6 load", killedPodName2), target, databaseSSELogger)

	databaseSSELogger.Info("Pod %s deleted under K6 load. Waiting for replacement to become ready.", killedPodName2)

	recoveryDuration2, err := kubernetesClient.WaitForRecovery(ctx, target, killedPodName2, killTime2, recoveryTimeout)

	if err != nil {

		databaseSSELogger.Error("Recovery detection (under K6 load) failed for %s/%s after %s timeout: %s",
			target.Namespace, target.Name, recoveryTimeout, err.Error())

		log.Printf("PodKillAndStartScenario execution: under-load recovery detection failed for %s/%s: %v", target.Namespace, target.Name, err)

		return err

	}

	recoverySeconds2 := recoveryDuration2.Seconds()

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Replacement pod (under K6 load) became healthy after %.1f seconds.", recoverySeconds2),
		testRunId, "Replacement pod healthy again", target, databaseSSELogger)

	databaseSSELogger.Info("Replacement pod (under K6 load) became healthy after %.1f seconds.", recoverySeconds2)

	// Wait after recovery so Prometheus captures post-recovery traffic
	// before K6 is stopped — ensures the dashboard shows the recovery ramp-up.
	databaseSSELogger.Info("Pod recovered; waiting %g seconds for Prometheus to capture post-recovery traffic before stopping K6.", warmupDuration.Seconds())
	time.Sleep(warmupDuration)

	if err := database.InsertTestRunFinding(ctx, db, testRunId,
		"POD_RECOVERY_READY_PASSED_UNDER_LOAD_TIME_SECONDS",
		fmt.Sprintf("%.2f", recoverySeconds2), "number"); err != nil {
		databaseSSELogger.Error("Could not persist under-load recovery time to database: %s", err.Error())
		log.Printf("PodKillAndStartScenario execution: could not persist under-load recovery time for run %d: %v", testRunId, err)
		return err
	}

	return nil
}

// Milestone 2
// File PodKillAndStartScenario-Implementation-Plan.md
func doPodKillRestartAutomaticallyReturnAndSaveToDatabaseBaseRecoveryTimeWithoutLoadTestOne(ctx context.Context,
	s *PodKillAndStartScenario, testRunId int64, target applicationModelStructs.Workload,
	databaseSSELogger *logger.DatabaseSSELogger, kubernetesClient *restClient.KubernetesClient, db *sql.DB) (time.Duration, error) {

	databaseSSELogger.Info("Going to execute test to delete a running Pod of workload %s WITHOUT system load now.", target.Name)

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Test kill pod without load of Test Scenario Run \"Pod Kill And Start\" RUN-%06d started", testRunId),
		testRunId, "Action:Test kill pod without load start", target, databaseSSELogger)

	defer func() {
		databaseSSELogger.Info("Test to delete a running Pod of workload %s WITHOUT system load finished.", target.Name)
		_ = addAnnotationToChart(ctx,
			fmt.Sprintf("Test kill pod without load of Test Scenario Run \"Pod Kill And Start\" RUN-%06d finished", testRunId),
			testRunId, "Action:Test kill pod without load end", target, databaseSSELogger)
	}()

	databaseSSELogger.Info("Going to delete a running Pod of workload %s now.", target.Name)

	// Milestone 1 of PodKillAndStartScenario-Implementation-Plan.md: Real pod deletion.
	// We delete one running and ready pod using SIGKILL (GracePeriodSeconds=0).
	// The workload's controller will automatically schedule a replacement pod.
	// Recovery detection (Milestone 2), HPA strategy split (Milestone 4+6),
	// and ensuring ≥2 replicas before killing (Milestone 6 Strategy A) are
	// implemented in subsequent milestones — see PodKillAndStartScenario-Implementation-Plan.md.
	// killTime is captured immediately after the delete API call succeeds inside
	// DeleteOnePodUnGracefully and is passed to WaitForRecovery so that recovery
	// duration is computed as:
	//   pod.Ready.LastTransitionTime  −  killTime
	// rather than using a polling-based elapsed time which carries up to 2s of error.
	killedPodName, killTime, err := kubernetesClient.DeleteOnePodUnGracefully(ctx, target)

	if err != nil {

		databaseSSELogger.Error("Could not delete a pod for workload %s/%s: %s", target.Namespace, target.Name, err.Error())

		log.Printf("PodKillAndStartScenario execution: could not delete pod for %s/%s: %v", target.Namespace, target.Name, err)

		//todo
		//Message later
		return 0, err

	}

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Pod %s deleted successfully. The workload controller is supposed to schedule a replacement now.", killedPodName),
		testRunId, fmt.Sprintf("Action:Pod %s killed", killedPodName), target, databaseSSELogger)

	databaseSSELogger.Info("Pod %s deleted successfully. The workload controller is supposed to schedule a replacement now.", killedPodName)

	// Milestone 2 of PodKillAndStartScenario-Implementation-Plan.md: Recovery detection.
	// Poll until a new Running+Ready pod appears for this workload, then log and
	// persist the recovery duration. On timeout the scenario fails hard — see
	// WaitForRecovery for the TODO on making this configurable in the future.
	const recoveryTimeout = 5 * time.Minute
	recoveryDuration, err := kubernetesClient.WaitForRecovery(ctx, target, killedPodName, killTime, recoveryTimeout)

	if err != nil {

		databaseSSELogger.Error("Recovery detection failed for workload %s/%s after %s timeout: %s",
			target.Namespace, target.Name, recoveryTimeout, err.Error())

		log.Printf("PodKillAndStartScenario execution: recovery detection failed for %s/%s after %s timeout: %v",
			target.Namespace, target.Name, recoveryTimeout, err)

		return 0, err

	}

	recoverySeconds := recoveryDuration.Seconds()

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Replacement pod became healthy after %.1f seconds.", recoverySeconds),
		testRunId, "Replacement pod became healthy", target, databaseSSELogger)

	databaseSSELogger.Info("Replacement pod became healthy after %.1f seconds.", recoverySeconds)

	if err := database.InsertTestRunFinding(ctx,
		db, testRunId, "POD_RECOVERY_READY_PASSED_NO_LOAD_TIME_SECONDS", fmt.Sprintf("%.2f", recoverySeconds), "number"); err != nil {

		// Non-fatal — the scenario succeeded, we just couldn't persist the metric.
		// Log it so it's visible but don't fail the run over it.
		databaseSSELogger.Error("Could not persist recovery time to database: %s", err.Error())
		log.Printf("PodKillAndStartScenario execution: could not persist recovery time for run %d: %v", testRunId, err)

		return 0, err

	}

	return recoveryTimeout, nil

}

func scaleToInitialNumberOfPods(kubernetesClient *restClient.KubernetesClient,
	target applicationModelStructs.Workload,
	scaleToReplicasAtTheEndAgain int32,
	databaseSSELogger *logger.DatabaseSSELogger) {

	scaleResultChannel := restClient.ScaleWorkload(context.Background(),
		kubernetesClient.KubernetesClientInterface,
		kubernetesClient.KubernetesClientDynamicInterface,
		restClient.ScaleTarget{
			Namespace:      target.Namespace,
			Name:           target.Name,
			DeploymentKind: target.Kind,
			Replicas:       scaleToReplicasAtTheEndAgain,
		},
		time.Minute*10)

	//Because of the channel
	//we can do this
	//here now after already
	//having started the action
	databaseSSELogger.Info("The number of pods get scaled again to %d as this was the number of replicas when the test run started.", scaleToReplicasAtTheEndAgain)

	if result := <-scaleResultChannel; result.Err == nil {

		databaseSSELogger.Info("Scaled the number of pods again to %d successfully.", scaleToReplicasAtTheEndAgain)

	} else {

		databaseSSELogger.Error("Replicas of %s/%s could not get scaled again to %d (original count) after the test: %s",
			target.Namespace, target.Name, scaleToReplicasAtTheEndAgain, result.Err.Error())

		log.Printf("PodKillAndStartScenario execution: scale-back failed for %s/%s: %v", target.Namespace, target.Name, result.Err)

	}

}

// createRecoveryLoadImpactVerdict compares the no-load and under-load pod recovery
// times recorded as findings during this run and writes a sub-verdict:
//
//	ratio = under_load / no_load
//	<= 1.05  → PASS     (load adds at most 5 % to recovery time)
//	< 1.10   → DEGRADED (load adds between 5 % and 10 %)
//	>= 1.10  → FAIL     (load adds 10 % or more to recovery time)
func (s *PodKillAndStartScenario) createRecoveryLoadImpactVerdict(
	ctx context.Context,
	testRunId int64,
	db *sql.DB,
	log *logger.DatabaseSSELogger,
) error {

	findings, err := database.GetTestRunFindings(ctx, db, testRunId)

	if err != nil {
		return fmt.Errorf("loading findings: %w", err)
	}

	var noLoad, underLoad float64
	var noLoadFound, underLoadFound bool

	for _, f := range findings {
		switch f.FindingTypeCode {
		case "POD_RECOVERY_READY_PASSED_NO_LOAD_TIME_SECONDS":
			if v, parseErr := strconv.ParseFloat(f.ResultValue, 64); parseErr == nil {
				noLoad = v
				noLoadFound = true
			}
		case "POD_RECOVERY_READY_PASSED_UNDER_LOAD_TIME_SECONDS":
			if v, parseErr := strconv.ParseFloat(f.ResultValue, 64); parseErr == nil {
				underLoad = v
				underLoadFound = true
			}
		}
	}

	if !noLoadFound || !underLoadFound {
		return fmt.Errorf("recovery load impact verdict: one or both findings missing for run %d (no_load=%v, under_load=%v)",
			testRunId, noLoadFound, underLoadFound)
	}

	ratio := 1.0

	if noLoad > 0 {
		ratio = underLoad / noLoad
	}

	var label string
	switch {
	case ratio <= 1.05:
		label = "PASS"
	case ratio < 1.10:
		label = "DEGRADED"
	default:
		label = "FAIL"
	}

	verdictText := fmt.Sprintf("no_load=%.2fs | under_load=%.2fs | ratio=%.2f", noLoad, underLoad, ratio)

	if err := database.WriteVerdict(ctx, db, testRunId, "POD_RECOVERY_LOAD_IMPACT", label, verdictText); err != nil {
		return fmt.Errorf("writing verdict: %w", err)
	}

	switch label {
	case "PASS":
		log.VerdictPass("Verdict [POD_RECOVERY_LOAD_IMPACT]: %s", verdictText)
	case "DEGRADED":
		log.VerdictDegraded("Verdict [POD_RECOVERY_LOAD_IMPACT]: %s", verdictText)
	default:
		log.VerdictFail("Verdict [POD_RECOVERY_LOAD_IMPACT]: %s", verdictText)
	}

	return nil

}

func (s *PodKillAndStartScenario) CreateFinalVerdicts(
	ctx context.Context,
	testRunId int64,
	target applicationModelStructs.Workload,
	db *sql.DB,
	sseLogger *logger.DatabaseSSELogger,
) error {

	verdicts, err := database.GetTestRunVerdictsOrderedByCreationTime(ctx, db, testRunId)

	if err != nil {
		return fmt.Errorf("loading verdicts for final summary: %w", err)
	}

	if len(verdicts) == 0 {
		sseLogger.Info("Final verdict: no sub-verdicts recorded for run %d.", testRunId)
		return nil
	}

	// Severity rank: higher = worse.
	rank := func(conclusion string) int {
		switch conclusion {
		case "FAIL":
			return 2
		case "DEGRADED":
			return 1
		default:
			return 0
		}
	}

	worst := verdicts[0]
	for _, v := range verdicts[1:] {
		if rank(v.VerdictConclusion) > rank(worst.VerdictConclusion) {
			worst = v
		}
		// Equal severity: keep the one recorded first (ordered by RECORDED_AT ASC).
	}

	msg := fmt.Sprintf("Final verdict for run %d: [%s] %s — %s (recorded at %s)",
		testRunId, worst.VerdictName, worst.VerdictConclusion, worst.VerdictText, worst.RecordedAt.Format("15:04:05"))
	switch rank(worst.VerdictConclusion) {
	case 2:
		sseLogger.VerdictFail("%s", msg)
	case 1:
		sseLogger.VerdictDegraded("%s", msg)
	default:
		sseLogger.VerdictPass("%s", msg)
	}

	return nil
}

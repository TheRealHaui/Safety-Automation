package scenarioImplementation

/*
File httpDependencyScenario.go contains the shared runner and VirtualService helpers
used by HttpDependencyDelayScenario and HttpDependencyErrorScenario. Both scenarios
follow the same lifecycle:

  Phase 0 — Dependency discovery: K6 drives traffic, Prometheus is polled to find
             outbound dependency hostnames.
  Phase 1 — Pre-fault warmup: K6 load job runs for warmupDuration so Prometheus
             captures a clean baseline before the fault is visible on the dashboard.
  Phase 2 — Fault injection: VirtualServices applied; fault runs for
             hddsSharedFaultDurationSeconds.
  Phase 3 — Post-fault observation: VirtualServices removed; K6 continues for
             warmupDuration so Prometheus captures the recovery ramp-up.

What differs between the two scenarios is captured in HttpDependencyFaultConfig.
*/

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	prometheusClient "github.com/TheRealHaui/Safety-Automation/internal/backend/prometheus"
	"github.com/TheRealHaui/Safety-Automation/internal/backend/restClient"
	"github.com/TheRealHaui/Safety-Automation/internal/logger"
)

// Shared timing constants for all HTTP dependency fault scenarios.
const (
	// hddsSharedDiscoveryTimeoutSeconds is the maximum time spent polling Prometheus
	// for new dependencies before giving up and proceeding with whatever was found.
	// Acts as a safety ceiling — normal stop is 10 consecutive polls with no new entries.
	hddsSharedDiscoveryTimeoutSeconds = 100

	// hddsSharedFaultDurationSeconds is the length of the fault injection window (Phase 2).
	// 2 minutes gives enough time for error budget consumption, retry storms,
	// circuit-breaker state transitions, and latency spikes to become visible on
	// the Grafana dashboard.
	hddsSharedFaultDurationSeconds = 120
)

// HttpDependencyFaultConfig captures what differs between HttpDependencyDelayScenario
// and HttpDependencyErrorScenario. Everything else is handled by runHttpDependencyScenario.
type HttpDependencyFaultConfig struct {
	// DisplayName is used in log messages and Grafana annotation text.
	// Example: "HTTP Dependency Delay", "HTTP Dependency Error"
	DisplayName string

	// FaultDescription is a short, human-readable description of what the fault
	// actually does, used in user-facing log lines and Grafana annotation text
	// alongside DisplayName so the observer knows exactly what is being injected.
	// Example: "2s latency on 100% of requests", "HTTP 503 on 50% of requests"
	FaultDescription string

	// K6ScenarioKey is the abbreviated scenario identifier used in K6 job names.
	// Must match one of the K6ScenarioXxx constants.
	K6ScenarioKey string

	// FaultBlock is the complete Istio fault: {...} block inserted into each
	// VirtualService http rule. The caller constructs the correct block for the
	// fault type (delay or abort).
	//
	// Delay example:
	//   map[string]interface{}{
	//     "delay": map[string]interface{}{
	//       "fixedDelay": "2s",
	//       "percentage": map[string]interface{}{"value": 100.0},
	//     },
	//   }
	//
	// Abort example:
	//   map[string]interface{}{
	//     "abort": map[string]interface{}{
	//       "httpStatus": int64(503),
	//       "percentage": map[string]interface{}{"value": 50.0},
	//     },
	//   }
	FaultBlock map[string]interface{}
}

// vsEntry holds everything needed to apply and later undo one VS change.
//
// isNew=true  → Safety Automation created the VS; cleanup deletes it.
// isNew=false → Safety Automation patched an existing VS; cleanup restores originalVS.
//
// This split allows the two cases to share identical apply/cleanup loop code
// while keeping the undo logic correct for each.
type vsEntry struct {
	name       string
	vs         *unstructured.Unstructured
	isNew      bool
	originalVS *unstructured.Unstructured // only set when isNew=false
}

// runHttpDependencyScenario is the shared implementation for all HTTP dependency
// fault injection scenarios. It handles dependency discovery, VS lifecycle,
// K6 job management, and the three-phase load test. What differs between scenarios
// (fault type, parameters, display name, K6 key) is passed via cfg.
func runHttpDependencyScenario(
	ctx context.Context,
	cfg HttpDependencyFaultConfig,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	kubernetesClient *restClient.KubernetesClient,
	db *sql.DB,
	databaseSSELogger *logger.DatabaseSSELogger,
) error {

	databaseSSELogger.Info("%s test scenario starting for workload %s/%s with %d configured target URL(s).",
		cfg.DisplayName, target.Namespace, target.Name, len(urls))

	// Read the Prometheus scrape interval live — same approach as PodKillAndStartScenario.
	// warmupDuration ensures at least two Prometheus scrapes of clean baseline traffic
	// before fault injection, making the spike clearly visible on the Grafana dashboard.
	// Falls back to 15s if the Grafana datasource proxy is unreachable.
	prometheusInterval := prometheusClient.FetchKubernetesPodsScrapeInterval(ctx)
	warmupDuration := 2*prometheusInterval + 2*time.Second

	databaseSSELogger.Info("Prometheus scrape interval is %g seconds; pre-fault warmup and post-fault observation will be %g seconds each (2 × interval + 2s).",
		prometheusInterval.Seconds(), warmupDuration.Seconds())

	allURLsForLog := FormatURLsForLog(urls)
	if len(urls) == 1 {
		databaseSSELogger.Info("The following target URL was configured: %s.", allURLsForLog)
	} else {
		databaseSSELogger.Info("The following %d target URLs were configured: %s.", len(urls), allURLsForLog)
	}

	// "Action: Test run started" and "Action: Test run ended" tags are matched by name in
	// the Safety Automation Dashboard annotation definitions. Tag naming follows the same convention as
	// PodKillAndStartScenario — Grafana strips the space after ":" in annotations,
	// so the tag values here intentionally omit that space. Do not change without
	// updating the Grafana dashboard annotation filter.
	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Test Scenario Run %q RUN-%06d started", cfg.DisplayName, testRunId),
		testRunId, fmt.Sprintf("Action:Test Scenario Run %q started", cfg.DisplayName), target, databaseSSELogger)

	// Wait so the "started" and "ended" annotations are not too close together on the
	// Grafana timeline — same rationale as PodKillAndStartScenario.
	time.Sleep(5 * time.Second)

	defer func() {
		time.Sleep(5 * time.Second)
		_ = addAnnotationToChart(ctx,
			fmt.Sprintf("Test Scenario Run %q RUN-%06d ended", cfg.DisplayName, testRunId),
			testRunId, fmt.Sprintf("Action:Test Scenario Run %q ended", cfg.DisplayName), target, databaseSSELogger)
	}()

	// ── Phase: Dependency discovery ───────────────────────────────────────────

	k6Image := os.Getenv("K6_IMAGE")
	if k6Image == "" {
		// K6_IMAGE is read from the environment rather than being threaded through
		// the entire call chain — same reasoning as in PodKillAndStartScenario.
		// The fallback keeps the default deterministic; an empty image name would
		// silently break the Kubernetes Job without a clear error.
		k6Image = "grafana/k6:latest"
	}

	knownDeps, discErr := discoverHttpDependencies(ctx, cfg, testRunId, target, urls, k6Image, kubernetesClient, databaseSSELogger)
	if discErr != nil {
		return discErr
	}

	depCount := len(knownDeps)

	if depCount == 0 {

		// A workload with no Istio-tracked outbound dependencies cannot be tested with
		// VirtualService fault injection. This is not an error — the workload simply has
		// no dependency edges to probe. Possible causes: no Istio sidecar on the
		// workload, all dependencies are external (filtered as "unknown"), or the workload
		// genuinely makes no outbound service calls during the test window.
		databaseSSELogger.Info("No dependencies discovered for workload %s/%s. The workload has no Istio-tracked outbound service calls within the test window. Finishing test successfully.", target.Namespace, target.Name)

		return nil

	}

	// ── Phase: Build VirtualServices ─────────────────────────────────────────
	//
	// One VirtualService per discovered dependency host. A single multi-host VS
	// cannot be used because each http rule's route.destination.host must be an
	// explicit hostname — Istio rejects the "*" wildcard. Multiple rules with the
	// same (empty) match in one VS also trigger Istio overlap warnings ("rule #N
	// not used") because Istio stops at the first matching rule. One VS per host
	// avoids both problems: each VS has exactly one host and one rule.
	//
	// The hoplon.io/managed-by=hoplon label on every new VS allows the startup orphan
	// scan (ListSafetyAutomationVirtualServiceNames) to find and delete all of them as a
	// group, even when there are multiple per run.

	vsEntries := make([]vsEntry, 0, depCount)
	depIdx := 0
	for _, dep := range knownDeps {
		existing, findErr := kubernetesClient.FindVirtualServiceForHost(ctx, dep.ServiceHostname, target.Namespace)
		if findErr != nil {
			// Non-fatal — fall back to creating a new VS so the test can still run.
			databaseSSELogger.Warn("Could not check for existing VirtualService for host %s (falling back to create): %s",
				dep.ServiceHostname, findErr.Error())
			log.Printf("%s: FindVirtualServiceForHost failed for %s run %d: %v", cfg.DisplayName, dep.ServiceHostname, testRunId, findErr)
		}

		if existing == nil {
			// ── Case 1: No pre-existing VS — create one ──────────────────────────
			// Safety Automation owns it entirely; cleanup simply deletes it.
			vsName := getVSNameForDependencyService(target.Name, depIdx, testRunId)
			vsEntries = append(vsEntries, vsEntry{
				name:  vsName,
				vs:    hddsBuildVirtualService(vsName, target.Namespace, testRunId, dep, cfg.FaultBlock),
				isNew: true,
			})
			databaseSSELogger.Info("No existing VirtualService for host %s — will create hoplon-managed VS %s.",
				dep.ServiceHostname, vsName)
		} else {
			// ── Case 2: Pre-existing VS — patch it in-place ──────────────────────
			// Add the fault block to every existing http rule so that the fault
			// fires regardless of which rule matches the traffic (first-match-wins).
			// A catch-all rule is appended at the end only if no existing rule is
			// already a catch-all. Cleanup restores the original VS exactly.
			//
			// ⚠ NOT TESTED YET — see https://trello.com/c/HnTfQynp
			patched, patchWarnings := hddsPatchExistingVS(existing, testRunId, cfg.FaultBlock)
			for _, w := range patchWarnings {
				databaseSSELogger.Warn("%s", w)
			}
			vsEntries = append(vsEntries, vsEntry{
				name:       existing.GetName(),
				vs:         patched,
				isNew:      false,
				originalVS: existing,
			})
			databaseSSELogger.Info("Found existing VirtualService %s for host %s — will patch it with fault injection (original saved for restore).",
				existing.GetName(), dep.ServiceHostname)
		}
		depIdx++
	}

	databaseSSELogger.Info("Prepared %d VirtualService change(s) for cascading %s (%s) injection (all callers of dependency hosts affected).",
		depCount, cfg.DisplayName, cfg.FaultDescription)

	// ── Phase: K6 load test ───────────────────────────────────────────────────
	//
	// K6 runs continuously through three phases:
	//   Phase 1 — Pre-fault warmup (warmupDuration): baseline traffic for Grafana
	//   Phase 2 — Fault injection (hddsSharedFaultDurationSeconds): VS active, user observes impact
	//   Phase 3 — Post-fault observation (warmupDuration): VS removed, recovery captured

	loadJobName := K6JobName(cfg.K6ScenarioKey, K6RunTypeLoad, testRunId)
	loadJobConfigMapName := K6ScriptNameFromK6JobName(loadJobName)

	loadOpts := restClient.K6JobOptions{
		TestRunID:     testRunId,
		Namespace:     target.Namespace,
		K6Image:       k6Image,
		Script:        GenerateK6Script(urls),
		ConfigMapName: loadJobConfigMapName,
		JobName:       loadJobName,
	}

	if err := kubernetesClient.StartK6Job(ctx, loadOpts); err != nil {
		databaseSSELogger.Error("Could not start K6 load job %s: %s", loadJobName, err.Error())
		log.Printf("%s: could not start K6 load job for run %d: %v", cfg.DisplayName, testRunId, err)
		return err
	}

	// K6 load job cleanup — runs regardless of outcome via defer.
	// Registered immediately after StartK6Job so it always fires even if a later
	// step returns an error. Same pattern as PodKillAndStartScenario.
	defer func() {
		kubernetesClient.DeleteK6JobAndConfigMap(context.Background(), loadJobName, loadJobConfigMapName, target.Namespace)
		_ = addAnnotationToChart(ctx,
			fmt.Sprintf("K6 load test for RUN-%06d ended", testRunId),
			testRunId, "Action:K6 Load Test end", target, databaseSSELogger)
	}()

	databaseSSELogger.Info("Waiting for K6 load job pod to reach Running phase.")

	if err := kubernetesClient.WaitForK6JobRunning(ctx, loadJobName, target.Namespace); err != nil {
		databaseSSELogger.Error("K6 load job pod did not reach Running phase: %s", err.Error())
		log.Printf("%s: K6 load pod never reached Running for run %d: %v", cfg.DisplayName, testRunId, err)
		return err
	}

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("K6 load test for RUN-%06d started", testRunId),
		testRunId, "Action:K6 Load Test start", target, databaseSSELogger)

	databaseSSELogger.Info("K6 load job is running.")

	// ── Phase 1: Pre-fault warmup ─────────────────────────────────────────────
	//
	// Wait warmupDuration before applying the VirtualService so Prometheus captures
	// at least two scrapes of clean baseline traffic. This makes the fault impact
	// clearly visible on the Grafana graph (before vs. after fault injection).
	databaseSSELogger.Info("Phase 1 — pre-fault warmup: waiting %g seconds before applying %s (%s).", warmupDuration.Seconds(), cfg.DisplayName, cfg.FaultDescription)

	time.Sleep(warmupDuration)

	// ── Phase 2: Fault injection ──────────────────────────────────────────────

	databaseSSELogger.Info("Applying %d VirtualService(s): %s — %s (cascading — all callers of dependency hosts affected).", depCount, cfg.DisplayName, cfg.FaultDescription)

	// VirtualService cleanup safety-net — registered BEFORE the apply loop so that
	// a partial failure (VS 0 applied, VS 1 fails → early return) does not leave
	// already-applied VSes injecting faults indefinitely.
	//
	// Safety: DeleteVirtualService ignores NotFound, so calling it for a VS that was
	// never created is harmless. RestoreVirtualService on an unpatched VS fetches the
	// current state and writes it back unchanged — also a no-op. Both cases are safe
	// to call even when the corresponding apply never ran.
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		for _, entry := range vsEntries {
			if entry.isNew {
				_ = kubernetesClient.DeleteVirtualService(cleanCtx, entry.name, target.Namespace)
			} else {
				_ = kubernetesClient.RestoreVirtualService(cleanCtx, entry.originalVS, target.Namespace)
			}
		}
	}()

	for _, entry := range vsEntries {
		var applyErr error
		if entry.isNew {
			applyErr = kubernetesClient.ApplyVirtualService(ctx, entry.vs, target.Namespace)
		} else {
			applyErr = kubernetesClient.UpdateVirtualService(ctx, entry.vs, target.Namespace)
		}
		if applyErr != nil {
			databaseSSELogger.Error("Could not apply VirtualService change for %s: %s", entry.name, applyErr.Error())
			log.Printf("%s: could not apply VS %s for run %d: %v", cfg.DisplayName, entry.name, testRunId, applyErr)
			return applyErr
		}
	}

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("%s started (%s): %d dependencies for RUN-%06d", cfg.DisplayName, cfg.FaultDescription, depCount, testRunId),
		testRunId, "Action:Fault injection started", target, databaseSSELogger)

	databaseSSELogger.Info("Phase 2 — %s active (%s): %d dependencies. Observing for %d seconds.",
		cfg.DisplayName, cfg.FaultDescription, depCount, hddsSharedFaultDurationSeconds)

	time.Sleep(time.Duration(hddsSharedFaultDurationSeconds) * time.Second)

	// ── Phase 3: Post-fault observation ──────────────────────────────────────
	//
	// Delete/restore VirtualServices first so traffic returns to normal. Then let K6
	// continue for warmupDuration so Prometheus captures the recovery ramp-up.
	// Without this wait the Grafana graph is cut off and the user cannot see
	// whether the service recovered cleanly.

	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
	for _, entry := range vsEntries {
		var cleanErr error
		if entry.isNew {
			cleanErr = kubernetesClient.DeleteVirtualService(deleteCtx, entry.name, target.Namespace)
		} else {
			cleanErr = kubernetesClient.RestoreVirtualService(deleteCtx, entry.originalVS, target.Namespace)
		}
		if cleanErr != nil {
			// Non-fatal — the deferred safety-net registered above will retry.
			databaseSSELogger.Warn("Could not restore/delete VirtualService %s in normal flow (deferred cleanup will retry): %s",
				entry.name, cleanErr.Error())
		}
	}
	deleteCancel()

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("%s ended for RUN-%06d", cfg.DisplayName, testRunId),
		testRunId, "Action:Fault injection ended", target, databaseSSELogger)

	databaseSSELogger.Info("Phase 3 — post-fault observation: %d VirtualService(s) deleted. Waiting %g seconds for Prometheus to capture recovery traffic "+
		"before stopping K6.", depCount, warmupDuration.Seconds())

	time.Sleep(warmupDuration)

	// K6 load job is stopped by the registered defer above when this function returns.
	databaseSSELogger.Info("%s completed successfully for workload %s/%s.", cfg.DisplayName, target.Namespace, target.Name)
	return nil
}

// ── Dependency discovery ──────────────────────────────────────────────────────

// discoverHttpDependencies starts a K6 discovery job, drives traffic through the
// workload so Istio's Envoy sidecar emits istio_requests_total metrics, and polls
// Prometheus every second to accumulate outbound dependency hostnames.
//
// Stopping criteria (whichever comes first):
//   - 10 consecutive polls with no new entries AND at least 20s have elapsed
//   - hddsSharedDiscoveryTimeoutSeconds total elapsed (safety ceiling)
//   - ctx cancelled (test run cancelled by user)
//
// Why K6 is needed: a workload may only call certain dependencies under load.
// K6 triggers realistic code paths and ensures all dependencies are surfaced,
// not just those reachable via background health checks or idle traffic.
//
// Returns the discovered dependency map (key = ServiceHostname). An empty map
// with nil error means the workload has no Istio-tracked outbound dependencies —
// the caller should treat this as a successful no-op and return nil.
func discoverHttpDependencies(
	ctx context.Context,
	cfg HttpDependencyFaultConfig,
	testRunId int64,
	target applicationModelStructs.Workload,
	urls []database.TestRunURL,
	k6Image string,
	kubernetesClient *restClient.KubernetesClient,
	databaseSSELogger *logger.DatabaseSSELogger,
) (map[string]prometheusClient.Dependency, error) {

	discoveryJobName := K6JobName(cfg.K6ScenarioKey, K6RunTypeDisc, testRunId)
	discoveryJobConfigMapName := K6ScriptNameFromK6JobName(discoveryJobName)

	discoveryOpts := restClient.K6JobOptions{
		TestRunID:     testRunId,
		Namespace:     target.Namespace,
		K6Image:       k6Image,
		Script:        GenerateK6Script(urls),
		ConfigMapName: discoveryJobConfigMapName,
		JobName:       discoveryJobName,
	}

	if err := kubernetesClient.StartK6Job(ctx, discoveryOpts); err != nil {
		databaseSSELogger.Error("Could not start K6 discovery job %s: %s", discoveryJobName, err.Error())
		log.Printf("%s: could not start K6 discovery job for run %d: %v", cfg.DisplayName, testRunId, err)
		return nil, err
	}

	databaseSSELogger.Info("Waiting for K6 discovery job pod to reach Running phase.")

	if err := kubernetesClient.WaitForK6JobRunning(ctx, discoveryJobName, target.Namespace); err != nil {
		// Pod never reached Running — clean up the job regardless of its state.
		kubernetesClient.DeleteK6JobAndConfigMap(context.Background(), discoveryJobName, discoveryJobConfigMapName, target.Namespace)
		databaseSSELogger.Error("K6 discovery job pod did not reach Running phase: %s", err.Error())
		log.Printf("%s: K6 discovery pod never reached Running for run %d: %v", cfg.DisplayName, testRunId, err)
		return nil, err
	}

	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Dependency discovery started for RUN-%06d", testRunId),
		testRunId, "Action:Dependency discovery started", target, databaseSSELogger)

	databaseSSELogger.Info("K6 discovery job is running. Polling Prometheus every second for outbound dependency data for a maximum of %d seconds.",
		hddsSharedDiscoveryTimeoutSeconds)

	// knownDeps accumulates discovered dependencies over the full poll window.
	// Once the first dependency is found, we start counting consecutive stable
	// polls. 10 consecutive stable polls + minimum 20s → stop. Before the first
	// dependency is found we keep polling (up to the timeout) because Prometheus
	// may not have scraped yet — the first scrape arrives up to one interval (≤15s)
	// after K6 starts.
	knownDeps := make(map[string]prometheusClient.Dependency) // key = ServiceHostname
	k6DiscStart := time.Now()
	consecutiveUnchanged := 0

	discoveryTicker := time.NewTicker(time.Second)
	discoveryTimeout := time.After(time.Duration(hddsSharedDiscoveryTimeoutSeconds) * time.Second)

discoveryLoop:
	for {
		select {
		case <-ctx.Done():
			discoveryTicker.Stop()
			kubernetesClient.DeleteK6JobAndConfigMap(context.Background(), discoveryJobName, discoveryJobConfigMapName, target.Namespace)
			return nil, ctx.Err()

		case <-discoveryTimeout:
			databaseSSELogger.Info("Dependency discovery timed out after %d seconds — proceeding with %d dependencies found.",
				hddsSharedDiscoveryTimeoutSeconds, len(knownDeps))
			break discoveryLoop

		case <-discoveryTicker.C:
			// Elapsed window: max(15, int(elapsed_seconds)) — the 15s floor matches
			// the minimum Prometheus scrape interval so the rate() window always covers
			// at least one scrape. The growing window ensures dependencies called early
			// in the K6 run are not missed on later polls.
			elapsed := max(15, int(time.Since(k6DiscStart).Seconds()))
			newDeps, err := prometheusClient.QueryOutboundDependencies(ctx, target.Name, target.Namespace, elapsed)
			if err != nil {
				// Transient Grafana/Prometheus failures are non-fatal — just skip this poll.
				// If Prometheus is down the whole time, the timeout handles exit.
				databaseSSELogger.Warn("Prometheus dependency query failed (will retry): %s", err.Error())
				continue
			}

			newInThisPoll := false
			for _, dep := range newDeps {
				if _, exists := knownDeps[dep.ServiceHostname]; !exists {
					knownDeps[dep.ServiceHostname] = dep
					newInThisPoll = true

					// Log immediately when first discovered — not on each subsequent poll
					// that returns the same dependency. This gives the user live feedback
					// in the browser log view as dependencies appear.
					databaseSSELogger.Info("Discovered new dependency: %s", dep.ServiceHostname)
				}
			}

			// Only start the "consecutive unchanged" countdown after the first dependency
			// is found. Before that, empty results may simply mean Prometheus has not
			// completed its first scrape of this K6 run — counting them would exit
			// discovery prematurely for workloads that genuinely have dependencies.
			if len(knownDeps) == 0 {
				consecutiveUnchanged = 0
				continue
			}

			if !newInThisPoll {
				consecutiveUnchanged++
				if consecutiveUnchanged >= 10 && int(time.Since(k6DiscStart).Seconds()) > 20 {
					databaseSSELogger.Info("No new dependencies found in 10 consecutive polls — dependency set is stable.")
					break discoveryLoop
				}
			} else {
				consecutiveUnchanged = 0
			}
		}
	}

	discoveryTicker.Stop()

	// Discovery K6 job has served its purpose — stop it.
	kubernetesClient.DeleteK6JobAndConfigMap(context.Background(), discoveryJobName, discoveryJobConfigMapName, target.Namespace)

	depCount := len(knownDeps)
	depNames := make([]string, 0, depCount)
	for hostname := range knownDeps {
		depNames = append(depNames, hostname)
	}
	sort.Strings(depNames)
	depNamesList := strings.Join(depNames, ", ")
	databaseSSELogger.Info("Dependency discovery complete — %d dependencies found: %s", depCount, depNamesList)
	_ = addAnnotationToChart(ctx,
		fmt.Sprintf("Dependency discovery complete — %d dependencies found for RUN-%06d: %s", depCount, testRunId, depNamesList),
		testRunId, "Action:Dependency discovery complete", target, databaseSSELogger)

	return knownDeps, nil

}

// ── VirtualService construction ───────────────────────────────────────────────

// getVSNameForDependencyService generates a valid Kubernetes resource name for one VirtualService.
// Format: hoplon-vs-<sanitized-workload>-<depIndex>-<runId>
//
// depIndex is the 0-based position of the dependency in the knownDeps iteration,
// making each VS name unique within a run without embedding the full hostname
// (which may contain dots and slashes that are illegal in Kubernetes names).
// The workload name is sanitized and truncated so the full name stays within
// Kubernetes' 63-character resource name limit.
func getVSNameForDependencyService(workloadName string, depIndex int, runId int64) string {
	const prefix = "hoplon-vs-"
	suffix := fmt.Sprintf("-%d-%d", depIndex, runId)
	maxWorkloadLen := 63 - len(prefix) - len(suffix)
	if maxWorkloadLen < 1 {
		maxWorkloadLen = 1
	}
	sanitized := hddsSanitizeName(workloadName)
	if len(sanitized) > maxWorkloadLen {
		sanitized = strings.TrimRight(sanitized[:maxWorkloadLen], "-")
	}
	return prefix + sanitized + suffix
}

// hddsSanitizeName converts s to a Kubernetes-safe name segment:
// lowercase, only alphanumeric and hyphens, no leading/trailing hyphens.
func hddsSanitizeName(s string) string {
	lower := strings.ToLower(s)
	var b strings.Builder
	for _, c := range lower {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// hddsPatchExistingVS takes a pre-existing VirtualService and returns a deep copy
// with the given faultBlock added to every http rule, plus a slice of warning strings
// for any conditions the caller should surface to the user.
//
// Why do we have to do this this way?
// Istio enforces a single VirtualService per host per namespace. Creating a second VS for the same host does not stack or merge — the behavior
// is undefined and in practice Maistra silently ignores the second VS, so the fault would never be injected.
//
// Adding a new fault rule to the existing VS without touching the existing rules does not work either, because Istio evaluates http rules strictly
// top-to-bottom and stops at the first match. A new rule appended at the end is dead if any existing rule above it matches all traffic (a catch-all),
// which is the common pattern. A new rule prepended at the beginning fires correctly but bypasses all existing routing logic — canary splits,
// header-based routing, retry policies — which are precisely the conditions we want to test under.
//
// The only correct approach is to inject the fault block directly into each existing rule. Because only one rule fires per request and the fault is now
// present in every rule, every request is affected regardless of which rule matches it. The existing routing and retry logic is fully preserved, so
// the test exercises the service under realistic production configuration.
// NOT TESTED YET — see https://trello.com/c/HnTfQynp
//
// Patching strategy:
//  1. For each existing http rule: add the faultBlock. Istio evaluates rules top-to-bottom,
//     first-match-wins — so whichever rule a request matches, it will carry the fault.
//     Rules that already have a fault block are skipped to avoid overriding pre-existing
//     fault configuration (a warning is emitted).
//  2. Catch-all rule: if NONE of the existing rules is a catch-all (a rule with no match
//     condition), a new catch-all rule is appended at the end. This covers traffic that
//     would not have matched any existing rule and would otherwise bypass fault injection.
//     If the last existing rule is already a catch-all, it was already patched in step 1 —
//     adding another would produce an Istio "rule #N not used" overlap warning.
//  3. Safety Automation labels are intentionally NOT added to the patched VS.
//     The startup orphan scan deletes VSes by hoplon.io/managed-by=hoplon — labelling a
//     patched VS would permanently destroy the application team's routing configuration.
//     See https://trello.com/c/oPZNF3VW for the durable-restore path.
func hddsPatchExistingVS(existing *unstructured.Unstructured, runId int64, faultBlock map[string]interface{}) (*unstructured.Unstructured, []string) {
	patched := existing.DeepCopy()
	var warnings []string

	// Read spec.hosts so we can build the catch-all route destination if needed.
	hosts, _, _ := unstructured.NestedStringSlice(patched.Object, "spec", "hosts")
	catchAllHost := ""
	if len(hosts) > 0 {
		catchAllHost = hosts[0]
	}

	// Read existing http rules.
	rawHTTP, _, _ := unstructured.NestedSlice(patched.Object, "spec", "http")

	hasCatchAll := false
	for i, rawRule := range rawHTTP {
		rule, ok := rawRule.(map[string]interface{})
		if !ok {
			continue
		}
		// A catch-all rule has no match field.
		if _, hasMatch := rule["match"]; !hasMatch {
			hasCatchAll = true
		}
		// Skip rules that already carry a fault block — overriding pre-existing fault
		// configuration would silently change the test conditions and confuse results.
		if _, hasFault := rule["fault"]; hasFault {
			warnings = append(warnings, fmt.Sprintf(
				"VirtualService %s rule #%d already has a fault block — skipping to avoid overriding pre-existing fault configuration.",
				existing.GetName(), i))
			continue
		}
		rule["fault"] = faultBlock
		rawHTTP[i] = rule
	}

	// Append a catch-all rule if none of the existing rules covers unmatched traffic.
	// Without it, requests that fall through all existing rules bypass the VS entirely
	// and reach the destination without any injected fault.
	if !hasCatchAll {
		catchAllRule := map[string]interface{}{
			"fault": faultBlock,
			// route is required by Istio even for fault-only rules. We route back to
			// the first host in spec.hosts, which is the destination this VS governs.
			// Multi-host VSes are uncommon for individual services; if catchAllHost is
			// empty (no hosts in spec), we skip the catch-all to avoid creating an
			// invalid rule.
			"route": []interface{}{
				map[string]interface{}{
					"destination": map[string]interface{}{
						"host": catchAllHost,
					},
				},
			},
		}
		if catchAllHost != "" {
			rawHTTP = append(rawHTTP, catchAllRule)
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"VirtualService %s has no hosts in spec.hosts — cannot append catch-all fault rule. Traffic not matched by existing rules will not be affected.",
				existing.GetName()))
		}
	}

	_ = unstructured.SetNestedSlice(patched.Object, rawHTTP, "spec", "http")

	// Intentionally NO hoplon.io/managed-by=hoplon label on patched VSes.
	//
	// The startup orphan scan (scanForOrphanVirtualServicesAndRemoveThemIfAppropriate
	// in main.go) finds Safety-Automation-managed VSes by that label and DELETES them. For VSes
	// that Safety Automation created from scratch (Case 1) deletion is correct — they belong to
	// Safety Automation entirely. For patched VSes (Case 2) deletion would permanently destroy
	// the application team's VirtualService, which is far worse than leaving the
	// fault active until an operator notices.
	//
	// Consequence: if Safety Automation crashes mid-test while a VS is patched, the fault remains
	// active and the orphan scan cannot clean it up. Manual intervention is required.
	// A proper durable-restore path (save original to a ConfigMap before patching,
	// restore from it on startup) is tracked in: https://trello.com/c/oPZNF3VW

	/*
		labels := patched.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["hoplon.io/managed-by"] = "hoplon"
		labels["hoplon.io/run-id"] = fmt.Sprintf("%d", runId)
		patched.SetLabels(labels)
	*/
	_ = runId // runId unused now that we do not label patched VSes; kept for signature consistency

	return patched, warnings
}

// hddsBuildVirtualService constructs one Istio VirtualService for one discovered
// dependency host using the caller-supplied faultBlock. Called once per dependency;
// the caller collects all VSes and applies them as a group.
//
// ── Why one VS per host, not one VS for all hosts ────────────────────────────
//
// a) route.destination.host must be an explicit hostname — Istio rejects the
//
//	"*" wildcard with an admission webhook error. Without a valid destination,
//	there is no way to write a single route rule that says "forward to the
//	original destination" across multiple hosts.
//
// b) Multiple http rules with identical (or empty) match conditions in one VS
//
//	trigger Istio overlap warnings ("rule #N not used") because Istio stops
//	evaluating at the first matching rule, making all subsequent rules dead.
//
// One VS per host avoids both: each VS has exactly one host in spec.hosts and
// one http rule whose route.destination.host is that same host. Clean, valid,
// no warnings.
//
// ── Cascading vs. Scoped fault injection ─────────────────────────────────────
//
//  1. Scoped (NOT used here):
//     Add a sourceLabels match to the http rule so the fault only fires when
//     traffic originates from the workload under test (e.g. app=checkout).
//     Intermediate calls (e.g. B→C when B handles a request triggered by A) are
//     NOT degraded. Less realistic: in a real network outage every hop is affected.
//     See GetWorkloadPodTemplateLabels in KubernetesClient_VirtualService.go
//     (currently commented out) for the implementation.
//
//  2. Cascading (THIS implementation):
//     No sourceLabels match is set. The fault applies to ALL traffic destined
//     for this dependency host, regardless of which pod initiates the call.
//     This simulates infrastructure-level degradation where every caller in the
//     dependency sub-graph is affected equally — a more realistic chaos scenario.
//     Downside: services unrelated to the workload under test that also call this
//     dependency will also be affected. Acceptable in a dedicated test namespace;
//     NOT acceptable in a shared production namespace.
func hddsBuildVirtualService(
	name, namespace string,
	runId int64,
	dep prometheusClient.Dependency,
	faultBlock map[string]interface{},
) *unstructured.Unstructured {

	// One http rule, no match block — matches all traffic destined for this host.
	// route.destination.host must equal the VS host; Istio forwards the traffic
	// unchanged (no redirect), only the fault is injected.
	httpRule := map[string]interface{}{
		"fault": faultBlock,
		// route is mandatory even for fault-only rules. The destination host is
		// set to the same host as spec.hosts so traffic is forwarded unchanged.
		"route": []interface{}{
			map[string]interface{}{
				"destination": map[string]interface{}{
					"host": dep.ServiceHostname,
				},
			},
		},
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "networking.istio.io/v1beta1",
			"kind":       "VirtualService",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					// hoplon.io/managed-by enables the startup orphan scan to find and
					// delete any VS left from a crashed previous run — even when multiple
					// VSes were created for the same run.
					"hoplon.io/managed-by": "hoplon",
					// hoplon.io/run-id ties the VS to a specific test run for audit trails.
					"hoplon.io/run-id": fmt.Sprintf("%d", runId),
				},
			},
			"spec": map[string]interface{}{
				// spec.hosts scopes this VS to traffic destined for this one dependency.
				// Istio ignores the VS entirely for any other destination host.
				"hosts": []interface{}{dep.ServiceHostname},
				"http":  []interface{}{httpRule},
			},
		},
	}
}

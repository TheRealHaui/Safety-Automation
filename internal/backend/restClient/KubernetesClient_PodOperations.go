package restClient

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

// DeleteOnePodUnGracefully finds one running and ready pod belonging to the target workload and
// deletes it with GracePeriodSeconds=0 (immediate SIGKILL) to simulate an ungraceful
// crash — not a graceful shutdown.
//
// A regular pod delete sends SIGTERM and waits for the grace period (typically 30s),
// which allows the pod to drain connections cleanly. GracePeriodSeconds=0 bypasses
// that entirely and kills the pod immediately — this simulates an OOM kill, hardware
// failure, or kernel panic, which is the realistic failure mode this scenario tests.
//
// The workload's controller (ReplicaSet for Deployments, ReplicationController for
// DeploymentConfigs) will automatically schedule a replacement pod. The caller should
// use the returned pod name and kill time to track when that replacement becomes healthy
// (see Milestone 2: Recovery Detection of file PodKillAndStartScenario-Implementation-Plan.md).
//
// Returns the name of the deleted pod, the time at which the delete API call was issued
// (used as the start of the recovery window — pass it to WaitForRecovery), or an error
// if no suitable pod was found or the deletion failed.
func (c *KubernetesClient) DeleteOnePodUnGracefully(ctx context.Context, target applicationModelStructs.Workload) (string, time.Time, error) {

	// Resolve the label selector for pods managed by this workload.
	// The Workload.Labels field holds the Deployment/DC metadata labels — those are
	// NOT the pod template selector. We must fetch the selector from the workload
	// definition in the cluster to correctly identify which pods belong to it.
	labelSelector, err := c.getPodLabelSelectorForWorkload(ctx, target)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not determine pod label selector for %s/%s: %w", target.Namespace, target.Name, err)
	}

	pods, err := c.KubernetesClientInterface.CoreV1().Pods(target.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("listing pods for workload %s/%s: %w", target.Namespace, target.Name, err)
	}

	// Pick the first pod that is Running and has all containers Ready.
	// We only kill healthy pods — killing an already-unhealthy pod does not
	// test resilience; it just piles on top of an existing problem.
	var podToDelete *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning && isPodReady(pod) {
			podToDelete = pod
			break
		}
	}

	if podToDelete == nil {
		return "", time.Time{}, fmt.Errorf("no running and ready pod found for workload %s/%s — cannot perform pod kill", target.Namespace, target.Name)
	}

	// GracePeriodSeconds=0: skip the SIGTERM + drain window and kill the pod immediately.
	gracePeriod := int64(0)
	err = c.KubernetesClientInterface.CoreV1().Pods(target.Namespace).Delete(ctx, podToDelete.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &gracePeriod,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("deleting pod %s: %w", podToDelete.Name, err)
	}

	// ── Kill time: why we re-fetch the pod and prefer DeletionTimestamp ──────
	//
	// All Kubernetes state — pod phase, conditions, timestamps — is stored in etcd
	// and written by the API server. This means DeletionTimestamp (set when the
	// delete request is processed) and the replacement pod's Ready condition
	// LastTransitionTime (set when the pod becomes healthy) both originate from
	// the same server-side clock. Subtracting them gives a recovery duration that
	// is internally consistent and free of clock skew between our process and the
	// cluster.
	//
	// We considered using the Kubernetes Event API (which also records pod lifecycle
	// events such as "Killing") but Events are best-effort: they can be dropped
	// under load, deduplicated, and have a short default retention (~1 hour).
	// DeletionTimestamp is authoritative state written atomically to etcd — it is
	// the right source for this measurement.
	//
	// The client-go Delete() call does not return the updated pod object, so we
	// immediately re-fetch the pod to read its server-assigned DeletionTimestamp.
	// With GracePeriodSeconds=0 the pod can be gone before the re-fetch completes —
	// in that case we fall back to time.Now(). The fallback introduces at most a
	// few milliseconds of error (one network round-trip), which is acceptable.
	killTime := time.Now() // fallback: used only if the re-fetch below fails
	if refetched, fetchErr := c.KubernetesClientInterface.CoreV1().Pods(target.Namespace).Get(ctx, podToDelete.Name, metav1.GetOptions{}); fetchErr == nil {
		if refetched.DeletionTimestamp != nil {
			// Use the authoritative server-side timestamp.
			killTime = refetched.DeletionTimestamp.Time
		}
		// DeletionTimestamp can be nil if the API server already removed the pod
		// object — in that case the time.Now() fallback above is used.
	}
	// If the re-fetch itself fails (e.g. 404 — pod already gone, or network error),
	// we also fall through to the time.Now() fallback. Either way recovery measurement
	// continues; we do not fail the scenario over a timestamp imprecision.

	return podToDelete.Name, killTime, nil
}

// WaitForRecovery polls until a replacement pod for the target workload becomes Running
// and Ready, then returns how long recovery took. The killedPodName is excluded from
// consideration — it may still be Terminating and must not be mistaken for the replacement.
//
// ── How recovery duration is measured ────────────────────────────────────────
//
// Recovery duration = Ready.LastTransitionTime − killTime
//
// Both timestamps come from the Kubernetes API server (backed by etcd), so they
// share the same clock source and the subtraction is internally consistent:
//
//	killTime            — the pod's DeletionTimestamp read back from the cluster
//	                      immediately after the delete call (see DeleteOnePodUnGracefully).
//	                      Falls back to time.Now() if the pod was already gone before
//	                      the re-fetch completed, introducing at most one round-trip of error.
//
//	LastTransitionTime  — the exact moment the API server recorded the replacement
//	                      pod's Ready condition transitioning to True. This is authoritative
//	                      state written atomically to etcd, not an approximation.
//
// We explicitly chose NOT to use the Kubernetes Event API (which also records lifecycle
// events such as "Killing" and "Started") because Events are best-effort: they can be
// dropped under cluster load, deduplicated, and have a short default retention (~1 hour).
// Condition timestamps written to the pod object are authoritative and persistent.
//
// We also chose NOT to use time.Since(start) for the recovery end because that would
// carry up to pollInterval (2s) of measurement error — we'd only know the pod was
// healthy on the next poll tick, not at the exact moment it transitioned.
//
// Polling interval is 2 seconds. The timeout is enforced via a child context derived from
// ctx, so an external cancellation (e.g. the user cancelling the test run) is also respected.
//
// On timeout the error is returned to the caller, which is expected to mark the test run
// as FAILED and stop execution.
// TODO: A future improvement would support partial results (persist what was collected so
// far) or make the timeout behaviour configurable per scenario rather than always failing hard.
func (kubernetsClient *KubernetesClient) WaitForRecovery(ctx context.Context, target applicationModelStructs.Workload, killedPodName string, killTime time.Time, timeout time.Duration) (time.Duration, error) {
	const pollInterval = 2 * time.Second

	labelSelector, err := kubernetsClient.getPodLabelSelectorForWorkload(ctx, target)
	if err != nil {
		return 0, fmt.Errorf("could not determine pod label selector for %s/%s: %w", target.Namespace, target.Name, err)
	}

	// Derive a child context that expires at the timeout deadline.
	// This also respects the parent ctx being cancelled (e.g. test run cancelled by operator).
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			if timeoutCtx.Err() == context.DeadlineExceeded {
				// TODO: configurable timeout behaviour — see function doc above.
				return 0, fmt.Errorf("recovery timeout (%s) exceeded waiting for a replacement pod for %s/%s after killing pod %s",
					timeout, target.Namespace, target.Name, killedPodName)
			}
			// Parent context cancelled (e.g. operator cancelled the test run).
			return 0, timeoutCtx.Err()

		case <-ticker.C:
			pods, err := kubernetsClient.KubernetesClientInterface.CoreV1().Pods(target.Namespace).List(timeoutCtx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				return 0, fmt.Errorf("listing pods while waiting for recovery of %s/%s: %w", target.Namespace, target.Name, err)
			}

			for i := range pods.Items {
				pod := &pods.Items[i]
				if pod.Name == killedPodName {
					// The killed pod may still be Terminating — skip it.
					continue
				}
				if pod.Status.Phase != corev1.PodRunning {
					continue
				}
				// Read the Ready condition's LastTransitionTime directly from the pod object.
				// Kubernetes records the exact moment the pod transitioned to Ready=True —
				// this is more precise than time.Since(start) which would carry up to
				// pollInterval (2s) of measurement error.
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						return condition.LastTransitionTime.Time.Sub(killTime), nil
					}
				}
			}
		}
	}
}

// getPodLabelSelectorForWorkload returns the Kubernetes label selector string for pods
// managed by the given workload.
//
// For Deployments:       uses spec.selector.matchLabels (standard Kubernetes field)
// For DeploymentConfigs: uses spec.selector (OpenShift-specific flat map[string]string)
//
// These are intentionally different from the workload's own metadata labels.
func (c *KubernetesClient) getPodLabelSelectorForWorkload(ctx context.Context, target applicationModelStructs.Workload) (string, error) {
	if target.Kind == applicationModelStructs.KindDeployment {
		dep, err := c.KubernetesClientInterface.AppsV1().Deployments(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("fetching Deployment %s/%s: %w", target.Namespace, target.Name, err)
		}
		return labels.Set(dep.Spec.Selector.MatchLabels).String(), nil
	}

	// DeploymentConfig: fetch via dynamic client — spec.selector is a flat map[string]string,
	// not the matchLabels/matchExpressions structure used by standard Deployments.
	item, err := c.KubernetesClientDynamicInterface.Resource(deploymentConfigGVR).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("fetching DeploymentConfig %s/%s: %w", target.Namespace, target.Name, err)
	}

	spec, ok := item.Object["spec"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("DeploymentConfig %s/%s has no spec", target.Namespace, target.Name)
	}
	selectorRaw, ok := spec["selector"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("DeploymentConfig %s/%s has no spec.selector", target.Namespace, target.Name)
	}

	labelSet := make(labels.Set, len(selectorRaw))
	for k, v := range selectorRaw {
		if strVal, ok := v.(string); ok {
			labelSet[k] = strVal
		}
	}
	return labelSet.String(), nil
}

// isPodReady returns true if the pod has a Ready condition with status True,
// meaning all containers are running and passing their readiness probes.
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

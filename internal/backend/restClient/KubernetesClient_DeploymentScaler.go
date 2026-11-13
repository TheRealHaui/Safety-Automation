package restClient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
	k8sappsv1 "k8s.io/api/apps/v1"
	k8metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// ScaleTarget identifies a workload to scale.
type ScaleTarget struct {
	Namespace string
	Name      string
	// DeploymentKind must be "Deployment" or "DeploymentConfig"
	DeploymentKind applicationModelStructs.OpenShiftWorkloadTypeStruct
	Replicas       int32
}

// ScaleResult is returned per target and can be received from the channel
// the caller gets back. It is sent exactly once per target — either on
// success or on timeout/error.
type ScaleResult struct {
	Target ScaleTarget
	// Ready is true when the observed replica count reached the desired value.
	Ready bool
	Err   error
}

// ScaleWorkloads patches every target to the requested replica count and
// returns a channel that emits one ScaleResult per target.
// The channel is closed after all results have been sent.
//
// timeout is the maximum time to wait for each individual target to
// reach the desired replica count before sending a timed-out ScaleResult.
//
// Requirements:
//   - k8sClient    – standard Kubernetes clientset
//   - dynamicClient – Kubernetes KubernetesClientDynamicInterface client (covers DeploymentConfigs as CRs)
func ScaleWorkloads(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	targets []ScaleTarget,
	timeout time.Duration,
) <-chan ScaleResult {

	results := make(chan ScaleResult, len(targets))

	go func() {
		defer close(results)

		var wg sync.WaitGroup

		for _, t := range targets {
			wg.Add(1)
			go func(target ScaleTarget) {
				defer wg.Done()
				result := scaleAndWatch(ctx, k8sClient, dynamicClient, target, timeout)
				results <- result
			}(t)
		}

		wg.Wait()
	}()

	return results
}

func ScaleWorkload(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	target ScaleTarget,
	timeout time.Duration,
) <-chan ScaleResult {
	return ScaleWorkloads(ctx, k8sClient, dynamicClient, []ScaleTarget{target}, timeout)
}

// scaleAndWatch patches a single workload and blocks until the desired
// replica count is observed (or the deadline is reached).
func scaleAndWatch(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	target ScaleTarget,
	timeout time.Duration,
) ScaleResult {

	// ── 1. Patch the replica count (scale only, no full overwrite) ──────────

	replicasPatch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, target.Replicas)

	switch target.DeploymentKind {
	case applicationModelStructs.KindDeployment:
		_, err := k8sClient.AppsV1().Deployments(target.Namespace).Patch(
			ctx,
			target.Name,
			types.MergePatchType,
			[]byte(replicasPatch),
			k8metav1.PatchOptions{},
		)
		if err != nil {
			return ScaleResult{Target: target, Err: fmt.Errorf("patch Deployment: %w", err)}
		}

	case applicationModelStructs.KindDeploymentConfig:
		_, err := dynamicClient.Resource(deploymentConfigGVR).Namespace(target.Namespace).Patch(
			ctx,
			target.Name,
			types.MergePatchType,
			[]byte(replicasPatch),
			k8metav1.PatchOptions{},
		)
		if err != nil {
			return ScaleResult{Target: target, Err: fmt.Errorf("patch DeploymentConfig: %w", err)}
		}

	default:
		return ScaleResult{
			Target: target,
			Err:    fmt.Errorf("unsupported kind %q (must be Deployment or DeploymentConfig)", target.DeploymentKind),
		}
	}

	// ── 2. Watch via an Informer until ready or timeout ─────────────────────

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ready := make(chan struct{}, 1)

	// replicasReady checks the observed available replicas.
	signalIfReady := func(availableReplicas, desiredReplicas int32) {
		if availableReplicas == desiredReplicas {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}

	switch target.DeploymentKind {
	case applicationModelStructs.KindDeployment:
		factory := informers.NewSharedInformerFactoryWithOptions(
			k8sClient,
			0,
			informers.WithNamespace(target.Namespace),
		)
		informer := factory.Apps().V1().Deployments().Informer()

		informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				d, ok := obj.(*k8sappsv1.Deployment)
				if ok && d.Name == target.Name {
					signalIfReady(d.Status.AvailableReplicas, target.Replicas)
				}
			},
			UpdateFunc: func(_, newObj interface{}) {
				d, ok := newObj.(*k8sappsv1.Deployment)
				if ok && d.Name == target.Name {
					signalIfReady(d.Status.AvailableReplicas, target.Replicas)
				}
			},
		})

		factory.Start(deadlineCtx.Done())
		factory.WaitForCacheSync(deadlineCtx.Done())

	case applicationModelStructs.KindDeploymentConfig:

		factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
			dynamicClient,
			0,
			target.Namespace,
			nil,
		)
		informer := factory.ForResource(deploymentConfigGVR).Informer()

		informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				if avail, ok := availableReplicasFromUnstructured(obj); ok {
					u := obj.(*unstructured.Unstructured)
					if u.GetName() == target.Name {
						signalIfReady(avail, target.Replicas)
					}
				}
			},
			UpdateFunc: func(_, newObj interface{}) {
				if avail, ok := availableReplicasFromUnstructured(newObj); ok {
					u := newObj.(*unstructured.Unstructured)
					if u.GetName() == target.Name {
						signalIfReady(avail, target.Replicas)
					}
				}
			},
		})

		factory.Start(deadlineCtx.Done())
		factory.WaitForCacheSync(deadlineCtx.Done())
	}

	// ── 3. Wait for the signal ───────────────────────────────────────────────

	select {
	case <-ready:
		return ScaleResult{Target: target, Ready: true}
	case <-deadlineCtx.Done():
		if ctx.Err() != nil {
			return ScaleResult{Target: target, Err: fmt.Errorf("parent context cancelled: %w", ctx.Err())}
		}
		return ScaleResult{
			Target: target,
			Err:    fmt.Errorf("timed out after %s waiting for %s/%s to reach %d replicas", timeout, target.DeploymentKind, target.Name, target.Replicas),
		}
	}
}

// availableReplicasFromUnstructured extracts status.availableReplicas from a
// KubernetesClientDynamicInterface informer object. Returns (0, true) when the field is absent (e.g.
// during scale-down to zero).
func availableReplicasFromUnstructured(obj interface{}) (int32, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return 0, false
	}
	val, found, err := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	if err != nil {
		return 0, false
	}
	if !found {
		// Field absent means zero available replicas (common during scale-down).
		return 0, true
	}
	return int32(val), true
}

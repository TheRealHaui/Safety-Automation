package restClient

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	// applicationModelStructs is imported for GetWorkloadPodTemplateLabels (currently
	// commented out — scoped fault injection). Remove this import if that method is
	// permanently deleted.
	_ "github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

// virtualServiceGVR is the Istio VirtualService GroupVersionResource.
// v1beta1 is Istio's stable networking API version and is supported by
// Maistra 2.x (the OpenShift Service Mesh variant used in this cluster).
var virtualServiceGVR = schema.GroupVersionResource{
	Group:    "networking.istio.io",
	Version:  "v1beta1",
	Resource: "virtualservices",
}

// ApplyVirtualService creates the given VirtualService in the cluster.
// Returns an error if creation fails; the caller is responsible for registering
// a deferred delete to handle the cleanup in case a later step fails.
func (c *KubernetesClient) ApplyVirtualService(ctx context.Context, vs *unstructured.Unstructured, namespace string) error {
	_, err := c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).Create(ctx, vs, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating VirtualService %q in namespace %q: %w", vs.GetName(), namespace, err)
	}
	return nil
}

// DeleteVirtualService removes a VirtualService by name from the cluster.
// "Not found" is silently ignored so this is safe to call idempotently — once
// explicitly during normal flow and again in the deferred safety-net cleanup.
func (c *KubernetesClient) DeleteVirtualService(ctx context.Context, name, namespace string) error {
	err := c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("deleting VirtualService %q in namespace %q: %w", name, namespace, err)
	}
	return nil
}

// FindVirtualServiceForHost returns the first non-Safety-Automation-managed VirtualService
// whose spec.hosts list contains the given hostname, or nil if none exists.
//
// Used before fault injection to detect pre-existing VSes for a dependency host.
// Safety-Automation-managed VSes (label hoplon.io/managed-by=hoplon) are excluded — they
// were created by a previous run that did not clean up and will be handled by
// the orphan scan, not by this lookup.
func (c *KubernetesClient) FindVirtualServiceForHost(ctx context.Context, host, namespace string) (*unstructured.Unstructured, error) {
	list, err := c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing VirtualServices in namespace %q: %w", namespace, err)
	}
	for _, item := range list.Items {
		// Skip Safety-Automation-managed VSes — those are orphans from a previous run.
		if item.GetLabels()["hoplon.io/managed-by"] == "hoplon" {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "hosts")
		for _, h := range hosts {
			if h == host {
				copy := item
				return &copy, nil
			}
		}
	}
	return nil, nil
}

// UpdateVirtualService replaces an existing VirtualService in the cluster.
// The VS object must have metadata.resourceVersion set (from a prior Get/List)
// to satisfy Kubernetes optimistic concurrency control.
func (c *KubernetesClient) UpdateVirtualService(ctx context.Context, vs *unstructured.Unstructured, namespace string) error {
	_, err := c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).Update(ctx, vs, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating VirtualService %q in namespace %q: %w", vs.GetName(), namespace, err)
	}
	return nil
}

// RestoreVirtualService restores a VirtualService to its pre-test state.
// It fetches the current resourceVersion (required by Kubernetes for updates),
// copies it onto the saved original, then issues an Update. If the VS was
// deleted during the test (e.g. by an operator), it is recreated instead.
func (c *KubernetesClient) RestoreVirtualService(ctx context.Context, original *unstructured.Unstructured, namespace string) error {
	current, err := c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).Get(ctx, original.GetName(), metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// VS was removed during the test — recreate from the saved original.
			// Clear resourceVersion so Kubernetes treats it as a new object.
			restore := original.DeepCopy()
			restore.SetResourceVersion("")
			_, err = c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).Create(ctx, restore, metav1.CreateOptions{})
			return err
		}
		return fmt.Errorf("getting current VirtualService %q for restore: %w", original.GetName(), err)
	}
	// Stamp the current resourceVersion onto the original before updating so
	// Kubernetes accepts the write without an optimistic concurrency conflict.
	restore := original.DeepCopy()
	restore.SetResourceVersion(current.GetResourceVersion())
	_, err = c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).Update(ctx, restore, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("restoring VirtualService %q: %w", original.GetName(), err)
	}
	return nil
}

// ListSafetyAutomationVirtualServiceNames returns the names of all VirtualServices labeled
// hoplon.io/managed-by=hoplon in the given namespace.
//
// Used at application startup for the orphan scan: any VS left from a previous
// run that crashed before cleanup would silently inject latency into live traffic.
// The hoplon.io/managed-by=hoplon label makes Safety-Automation-created VSes uniquely
// identifiable so startup can safely delete any it finds.
func (c *KubernetesClient) ListSafetyAutomationVirtualServiceNames(ctx context.Context, namespace string) ([]string, error) {
	list, err := c.KubernetesClientDynamicInterface.Resource(virtualServiceGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "hoplon.io/managed-by=hoplon",
	})
	if err != nil {
		return nil, fmt.Errorf("listing Safety Automation VirtualServices in namespace %q: %w", namespace, err)
	}

	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names, nil
}

// GetWorkloadPodTemplateLabels is kept for the scoped fault injection approach
// and is currently unused. See hddsBuildVirtualService in HttpDependencyDelayScenario.go
// for the full explanation of scoped vs. cascading fault injection and why the
// cascading approach (no sourceLabels) was chosen instead.
//
// Uncomment the call site in HttpDependencyDelayScenario.go (Phase: Build VirtualService)
// and pass the returned labels to hddsBuildVirtualService to switch back to scoped.
/*
func (c *KubernetesClient) GetWorkloadPodTemplateLabels(
	ctx context.Context,
	name, namespace string,
	kind applicationModelStructs.OpenShiftWorkloadTypeStruct,
) (map[string]string, error) {

	if kind == applicationModelStructs.KindDeployment {
		d, err := c.KubernetesClientInterface.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting Deployment %q/%q: %w", namespace, name, err)
		}
		return d.Spec.Template.Labels, nil
	}

	// DeploymentConfig — no typed client available; use the dynamic interface.
	item, err := c.KubernetesClientDynamicInterface.Resource(deploymentConfigGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting DeploymentConfig %q/%q: %w", namespace, name, err)
	}

	// Navigate: spec.template.metadata.labels
	labels := make(map[string]string)
	spec, ok := item.Object["spec"].(map[string]interface{})
	if !ok {
		return labels, nil
	}
	template, ok := spec["template"].(map[string]interface{})
	if !ok {
		return labels, nil
	}
	metadata, ok := template["metadata"].(map[string]interface{})
	if !ok {
		return labels, nil
	}
	rawLabels, ok := metadata["labels"].(map[string]interface{})
	if !ok {
		return labels, nil
	}
	for k, v := range rawLabels {
		if sv, ok := v.(string); ok {
			labels[k] = sv
		}
	}
	return labels, nil
}
*/

// CountOtherSafetyAutomationRunningPods returns how many Safety Automation application pods are
// currently in Running phase in the given namespace, excluding the calling pod.
//
// Used before the orphan VirtualService scan at startup: if another Safety Automation
// instance is already running (rolling update, canary, or accidental duplicate),
// we must NOT delete its VirtualServices — they may belong to an active scenario.
//
// "Other instance" means: another Running pod of OUR OWN Deployment, from any of
// its ReplicaSets — during a rolling update the old pod belongs to the previous
// ReplicaSet, and that is exactly the pod that may still be running a scenario.
// Ourselves are excluded via os.Hostname(), which in Kubernetes is the pod's name.
//
// Why not a label selector: the former selector "app=hoplon" matched no pod of a
// Helm install (the chart labels pods app.kubernetes.io/name=<chart name>), so the
// count was always 0 and a freshly started pod deleted the VirtualServices of a
// scenario still running on the old pod. Matching by owning Deployment works for
// every install method and every chart/release name.
//
// Returns an error when our own Deployment is unknown (outside the cluster, or
// detection failed at startup). The caller then skips the orphan scan — deleting
// nothing is the safe default when we cannot tell whose VirtualServices they are.
func (c *KubernetesClient) CountOtherSafetyAutomationRunningPods(ctx context.Context, namespace string) (int, error) {
	if c.SelfDeploymentName == "" {
		return 0, fmt.Errorf("own Deployment name unknown (running outside the cluster, or detection failed at startup)")
	}

	currentPodName, err := os.Hostname()
	if err != nil {
		return 0, fmt.Errorf("could not determine current pod name via os.Hostname(): %w", err)
	}

	// Every pod created by a Deployment carries this label; it narrows the list
	// server-side without depending on chart-specific labels.
	pods, err := c.KubernetesClientInterface.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "pod-template-hash",
	})
	if err != nil {
		return 0, fmt.Errorf("listing Safety Automation pods in namespace %q: %w", namespace, err)
	}

	count := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Name == currentPodName || deploymentNameOf(pod) != c.SelfDeploymentName {
			// Ourselves, or a pod of some other application.
			continue
		}
		if pod.Status.Phase == corev1.PodRunning {
			count++
		}
	}
	return count, nil
}

package restClient

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

// KubernetesClient wraps Kubernetes client-go clients and exposes the current namespace.
type KubernetesClient struct {
	KubernetesClientInterface        kubernetes.Interface
	KubernetesClientDynamicInterface dynamic.Interface
	Namespace                        string

	// SelfDeploymentName is the name of the Deployment this process runs in, resolved
	// once at startup by detectSelfDeploymentName. Empty when running outside the
	// cluster (local development) or when it could not be determined.
	SelfDeploymentName string
}

// New initialises the OpenShift client from a kubeconfig path (or in-cluster config if empty).
func New(kubeconfigPath string) (*KubernetesClient, error) {
	var cfg *rest.Config
	var err error

	//We test first if
	//we execute on OpenShift
	cfg, err = rest.InClusterConfig()
	inCluster := err == nil

	if err != nil {

		err = nil

		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)

		if err == nil {
			log.Println("Application runs OUTSIDE OpenShift.")
		}

	} else {
		log.Println("Application is running inside OpenShift.")
	}

	if err != nil {
		return nil, fmt.Errorf("building kube config: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating KubernetesClientDynamicInterface client: %w", err)
	}

	namespace := detectNamespace(kubeconfigPath)

	// Outside the cluster (developer laptop) this process is not a Deployment,
	// so there is nothing to exclude from the workload list.
	selfDeploymentName := ""
	if inCluster {
		selfDeploymentName = detectSelfDeploymentName(k8sClient, namespace)
	}

	return &KubernetesClient{
		KubernetesClientInterface:        k8sClient,
		KubernetesClientDynamicInterface: dynClient,
		Namespace:                        namespace,
		SelfDeploymentName:               selfDeploymentName,
	}, nil
}

// detectSelfDeploymentName returns the name of the Deployment this pod belongs to,
// or "" if it cannot be determined.
//
// Why at runtime instead of a hardcoded name: the Deployment's name depends on how
// the app was installed. The Helm chart names it "<release>-<chart>" (e.g.
// "hoplon-dynacov-safety-automation"), an `oc start-build` install names it after
// the BuildConfig (e.g. "hoplon"). The former hardcoded filter "metadata.name!=hoplon"
// therefore silently failed for every Helm install, and the app listed ITSELF as a
// test target — a user could have pod-killed the tool while it was running a test.
//
// How: os.Hostname() is the pod name. A Deployment's pod is owned by a ReplicaSet
// that the Deployment controller always names "<deployment-name>-<pod-template-hash>",
// and the pod carries the same hash in its "pod-template-hash" label. Stripping that
// suffix yields the Deployment name exactly. This only needs the "get pods" RBAC
// permission the app already has — deliberately no extra rights (e.g. reading
// ReplicaSets), because "no elevated privileges" is a core promise of the product.
// deploymentNameOf returns the name of the Deployment that owns the given pod, or ""
// if the pod is not owned by a Deployment's ReplicaSet. See detectSelfDeploymentName
// for why this naming rule is exact. Shared with CountOtherSafetyAutomationRunningPods
// so "which Deployment is this pod from" is decided in exactly one place.
func deploymentNameOf(pod *corev1.Pod) string {
	hash := pod.Labels["pod-template-hash"]
	if hash == "" {
		return ""
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" && strings.HasSuffix(ref.Name, "-"+hash) {
			return strings.TrimSuffix(ref.Name, "-"+hash)
		}
	}
	return ""
}

func detectSelfDeploymentName(client kubernetes.Interface, namespace string) string {
	podName, err := os.Hostname()
	if err != nil {
		log.Printf("WARN: could not determine own pod name via os.Hostname(): %v — this application will appear in its own workload list", err)
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		log.Printf("WARN: could not read own pod %s/%s: %v — this application will appear in its own workload list", namespace, podName, err)
		return ""
	}

	if name := deploymentNameOf(pod); name != "" {
		log.Printf("Own Deployment detected as %q — it is excluded from the workload list.", name)
		return name
	}

	// E.g. deployed as a DeploymentConfig or a bare pod — neither is the supported
	// install method, so we warn instead of failing startup.
	log.Printf("WARN: own pod %s/%s is not owned by a Deployment's ReplicaSet — this application will appear in its own workload list", namespace, podName)
	return ""
}

// detectNamespace resolves the active namespace from kubeconfig context,
// POD_NAMESPACE env var, or falls back to "default".
func detectNamespace(kubeconfigPath string) string {

	// 1. Downward API (recommended)
	//    Which actually is an environment variable that gets
	//    injected by OpenShift at container start time
	//    if defined correctly in the application.yaml file
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}

	// 2. Running inside Kubernetes/OpenShift
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err == nil {
		return strings.TrimSpace(string(data))
	}

	// 3. Local kubeconfig namespace
	if kubeconfigPath != "" {

		config, err := clientcmd.LoadFromFile(kubeconfigPath)
		if err == nil {

			ctx := config.Contexts[config.CurrentContext]
			if ctx != nil && ctx.Namespace != "" {
				return ctx.Namespace
			}
		}
	}

	// 4. fallback
	return "default"

}

// GetOpenShiftServerVersion As the function name says. Additionally using its error return value
// use it to verify if a connection to the remote OpenShift server can get established
func (c *KubernetesClient) GetOpenShiftServerVersion() (*version.Info, error) {
	//When using the Interface
	//we have to go
	//over Discovery() we ever
	//To keep in mind
	return c.KubernetesClientInterface.Discovery().ServerVersion()
}

// GetAllWorkloads returns all Deployments and DeploymentConfigs in the namespace.
func (c *KubernetesClient) GetAllWorkloads(ctx context.Context) ([]applicationModelStructs.Workload, error) {

	var workloads []applicationModelStructs.Workload

	//Get Deployments
	//We exclude our own application here on purpose,
	//so nobody can run a test scenario against the tool itself.
	//The name is resolved at startup, see detectSelfDeploymentName.
	listOptions := metav1.ListOptions{}
	if c.SelfDeploymentName != "" {
		listOptions.FieldSelector = "metadata.name!=" + c.SelfDeploymentName
	}
	deps, err := c.KubernetesClientInterface.AppsV1().Deployments(c.Namespace).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	for i := range deps.Items {
		workloads = append(workloads, deploymentToWorkload(&deps.Items[i]))
	}

	//Get DeploymentConfigs
	dcList, err := c.KubernetesClientDynamicInterface.Resource(deploymentConfigGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{
		//No self-exclusion needed here: this application
		//is always deployed as a Deployment and never
		//as a legacy DeploymentConfig.
	})
	if err != nil {
		fmt.Printf("WARN: could not list DeploymentConfigs: %v\n", err)
	} else {
		for _, item := range dcList.Items {
			meta := item.Object["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			var replicas, readyReplicas int32
			if spec, ok := item.Object["spec"].(map[string]any); ok {
				if r, ok := spec["replicas"].(int64); ok {
					replicas = int32(r)
				}
			}
			if status, ok := item.Object["status"].(map[string]any); ok {
				if r, ok := status["readyReplicas"].(int64); ok {
					readyReplicas = int32(r)
				}
			}
			workloads = append(workloads, applicationModelStructs.Workload{
				Name: name, Namespace: c.Namespace,
				Kind: applicationModelStructs.KindDeploymentConfig, Replicas: replicas, ReadyReplicas: readyReplicas,
			})
		}
	}
	return workloads, nil
}

// GetSingleWorkloadDetails fetches a single workload by name and kind.
func (c *KubernetesClient) GetSingleWorkloadDetails(ctx context.Context, name string, kind applicationModelStructs.OpenShiftWorkloadTypeStruct) (*applicationModelStructs.Workload, error) {
	if kind == applicationModelStructs.KindDeployment {
		d, err := c.KubernetesClientInterface.AppsV1().Deployments(c.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		w := deploymentToWorkload(d)
		return &w, nil
	}
	item, err := c.KubernetesClientDynamicInterface.Resource(deploymentConfigGVR).Namespace(c.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	w := applicationModelStructs.Workload{Name: name, Namespace: c.Namespace, Kind: applicationModelStructs.KindDeploymentConfig}
	if spec, ok := item.Object["spec"].(map[string]any); ok {
		if r, ok := spec["replicas"].(int64); ok {
			w.Replicas = int32(r)
		}
	}
	if status, ok := item.Object["status"].(map[string]any); ok {
		if r, ok := status["readyReplicas"].(int64); ok {
			w.ReadyReplicas = int32(r)
		}
	}
	return &w, nil
}

func deploymentToWorkload(d *appsv1.Deployment) applicationModelStructs.Workload {
	w := applicationModelStructs.Workload{
		Name: d.Name, Namespace: d.Namespace, Kind: applicationModelStructs.KindDeployment,
		Replicas: derefInt32(d.Spec.Replicas), ReadyReplicas: d.Status.ReadyReplicas,
		Labels: d.Labels, CreationTime: d.CreationTimestamp.Time,
	}
	if len(d.Spec.Template.Spec.Containers) > 0 {
		w.Image = d.Spec.Template.Spec.Containers[0].Image
	}
	return w
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

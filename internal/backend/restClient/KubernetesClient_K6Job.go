package restClient

import (
	"context"
	"fmt"
	"io"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// K6JobOptions configures a K6 Kubernetes Job.
type K6JobOptions struct {
	TestRunID     int64
	Namespace     string
	K6Image       string
	Script        string
	ConfigMapName string
	JobName       string
}

// StartK6Job creates a ConfigMap containing the K6 script and a Job to execute it.
func (c *KubernetesClient) StartK6Job(ctx context.Context, opts K6JobOptions) error {

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: opts.ConfigMapName, Namespace: opts.Namespace,
			Labels: map[string]string{"app": "resilience-tester", "test-run-id": fmt.Sprintf("%d", opts.TestRunID)},
		},
		Data: map[string]string{"script.js": opts.Script},
	}
	if _, err := c.KubernetesClientInterface.CoreV1().ConfigMaps(opts.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating k6 configmap: %w", err)
	}

	backoff := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: opts.JobName, Namespace: opts.Namespace,
			Labels: map[string]string{"app": "resilience-tester", "test-run-id": fmt.Sprintf("%d", opts.TestRunID)},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						// Explicitly request Istio sidecar injection so K6 source-side
						// metrics (istio_requests_total with reporter="source") are
						// available regardless of whether the customer's OSSM control
						// plane uses auto-inject or opt-in injection policy.
						// Without this annotation, clusters configured for opt-in
						// injection would run K6 without an Envoy sidecar — making
						// client-side error visibility (503s during pod kill) invisible
						// in the Grafana dashboard. The development cluster has
						// autoInject=true and would work without this therefore, but
						// a  product must not rely on a specific customer
						// cluster configuration.
						// Further, advantage of that approach is that we will
						// probably use the complete same approach for Gattling
						// tests when customers use their own test suites.
						// Update: Even in development OpenShift needed.
						// Even more important to keep therefore.
						"sidecar.istio.io/inject": "true",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Volumes: []corev1.Volume{{
						Name: "k6-script",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: opts.ConfigMapName},
							},
						},
					}},
					Containers: []corev1.Container{{
						Name: "k6", Image: opts.K6Image,
						Args: []string{"run", "/scripts/script.js"},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "k6-script", MountPath: "/scripts"},
						},
					}},
				},
			},
		},
	}
	if _, err := c.KubernetesClientInterface.BatchV1().Jobs(opts.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		_ = c.KubernetesClientInterface.CoreV1().ConfigMaps(opts.Namespace).Delete(context.Background(), opts.ConfigMapName, metav1.DeleteOptions{})
		return fmt.Errorf("creating k6 job: %w", err)
	}
	return nil
}

// DeleteK6JobAndConfigMap removes the K6 Job and its ConfigMap (best-effort).
func (c *KubernetesClient) DeleteK6JobAndConfigMap(ctx context.Context, jobName, cmName, namespace string) {
	propagation := metav1.DeletePropagationForeground
	_ = c.KubernetesClientInterface.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &propagation})
	_ = c.KubernetesClientInterface.CoreV1().ConfigMaps(namespace).Delete(ctx, cmName, metav1.DeleteOptions{})
}

// WatchJobUntilDone blocks until the job succeeds, fails, or ctx is cancelled.
func (c *KubernetesClient) WatchJobUntilDone(ctx context.Context, jobName, namespace string) error {
	watcher, err := c.KubernetesClientInterface.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
	})
	if err != nil {
		return fmt.Errorf("watching job %s: %w", jobName, err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed for job %s", jobName)
			}
			job, ok := event.Object.(*batchv1.Job)
			if !ok {
				continue
			}
			if job.Status.Succeeded > 0 {
				return nil
			}
			if job.Status.Failed > 0 {
				return fmt.Errorf("k6 job %s failed", jobName)
			}
		}
	}
}

// StreamK6JobLogs follows the k6 container logs for the Job and writes them to out.
func (c *KubernetesClient) StreamK6JobLogs(ctx context.Context, jobName, namespace string, out io.Writer) error {
	podName, err := c.waitForJobPod(ctx, jobName, namespace)
	if err != nil {
		return err
	}

	req := c.KubernetesClientInterface.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow:    true,
		Container: "k6",
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("streaming k6 logs for pod %s: %w", podName, err)
	}
	defer stream.Close()

	if _, err := io.Copy(out, stream); err != nil {
		return fmt.Errorf("copying k6 logs for pod %s: %w", podName, err)
	}
	return nil
}

// WaitForK6JobRunning blocks until the K6 job's pod reaches Running phase.
// This is reliable compared to a fixed sleep after this or that time.
// Which of course is not reliable.
// StartK6Job needs its time until the job actually was started. This because
// image pulls can take 30+ seconds on a cold node.
// That is how Kubernetes works!
// We set the desired state.
// However, only OpenShift internals shift the current state over to
// the desired state.
//
// The pod phase is read from the Kubernetes API server, which reflects the
// kubelet's reported state stored in etcd. Running means the container process
// has started; K6 will be generating load within its first iteration (≤ 1s).
//
// Returns an error if ctx is cancelled, the pod fails to start, or the pod
// reaches a terminal phase (Succeeded/Failed) before we could measure.
func (c *KubernetesClient) WaitForK6JobRunning(ctx context.Context, jobName, namespace string) error {
	labelSelector := fmt.Sprintf("job-name=%s", jobName)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		pods, err := c.KubernetesClientInterface.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return fmt.Errorf("listing pods for k6 job %s: %w", jobName, err)
		}
		if len(pods.Items) > 0 {
			phase := pods.Items[0].Status.Phase
			switch phase {
			case corev1.PodRunning:
				return nil
			case corev1.PodFailed, corev1.PodSucceeded:
				// Terminal before we could use it — image crash, OOMKill, etc.
				return fmt.Errorf("k6 job pod reached terminal phase %s before entering Running", phase)
			}
			// PodPending or unknown — keep polling.
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *KubernetesClient) waitForJobPod(ctx context.Context, jobName, namespace string) (string, error) {
	labelSelector := fmt.Sprintf("job-name=%s", jobName)
	// Quick poll loop; jobs are short-lived and only create one pod.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		pods, err := c.KubernetesClientInterface.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return "", fmt.Errorf("listing pods for job %s: %w", jobName, err)
		}
		if len(pods.Items) > 0 {
			pod := pods.Items[0]
			if pod.Status.Phase != corev1.PodPending {
				return pod.Name, nil
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

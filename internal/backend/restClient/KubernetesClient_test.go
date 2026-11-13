package restClient

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDetectSelfDeploymentName covers the install methods that produce different
// Deployment names. The pod is created under this machine's hostname, because
// detectSelfDeploymentName identifies its own pod via os.Hostname() — exactly as
// in a real cluster, where the kubelet sets the hostname to the pod name.
func TestDetectSelfDeploymentName(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	const ns = "demo"

	tests := []struct {
		name   string
		pod    *corev1.Pod // nil = own pod does not exist
		expect string
	}{
		{
			name:   "Helm install: <release>-<chart>",
			pod:    ownedPod(hostname, ns, "hoplon-dynacov-safety-automation-7d4b9c8f6", "7d4b9c8f6"),
			expect: "hoplon-dynacov-safety-automation",
		},
		{
			name:   "oc start-build install: BuildConfig name",
			pod:    ownedPod(hostname, ns, "hoplon-5f6d7c9b8", "5f6d7c9b8"),
			expect: "hoplon",
		},
		{
			name:   "Deployment name containing dashes and digits",
			pod:    ownedPod(hostname, ns, "my-app-2-6c8f9d7b5", "6c8f9d7b5"),
			expect: "my-app-2",
		},
		{
			name:   "bare pod without owner",
			pod:    &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: hostname, Namespace: ns}},
			expect: "",
		},
		{
			name:   "owner name does not end with pod-template-hash",
			pod:    ownedPod(hostname, ns, "something-else", "7d4b9c8f6"),
			expect: "",
		},
		{
			name:   "own pod not found (e.g. missing RBAC or wrong namespace)",
			pod:    nil,
			expect: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset()
			if tc.pod != nil {
				client = fake.NewClientset(tc.pod)
			}
			if got := detectSelfDeploymentName(client, ns); got != tc.expect {
				t.Errorf("detectSelfDeploymentName() = %q, want %q", got, tc.expect)
			}
		})
	}
}

// ownedPod builds a pod the way the Deployment controller does: owned by a
// ReplicaSet named "<deployment>-<hash>" and labelled with that hash.
func ownedPod(name, namespace, replicaSetName, hash string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:            name,
		Namespace:       namespace,
		Labels:          map[string]string{"pod-template-hash": hash},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSetName}},
	}}
}

// TestCountOtherSafetyAutomationRunningPods guards the orphan-VirtualService cleanup:
// a freshly started pod must see the OLD pod of a rolling update (different
// ReplicaSet, same Deployment), or it deletes the VirtualServices of a scenario
// that is still running there.
func TestCountOtherSafetyAutomationRunningPods(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	const ns, self = "demo", "hoplon-dynacov-safety-automation"

	running := func(p *corev1.Pod) *corev1.Pod { p.Status.Phase = corev1.PodRunning; return p }
	pending := func(p *corev1.Pod) *corev1.Pod { p.Status.Phase = corev1.PodPending; return p }

	tests := []struct {
		name     string
		selfName string
		pods     []*corev1.Pod
		expect   int
		wantErr  bool
	}{
		{
			name:     "alone: only ourselves",
			selfName: self,
			pods:     []*corev1.Pod{running(ownedPod(hostname, ns, self+"-newhash1", "newhash1"))},
			expect:   0,
		},
		{
			name:     "rolling update: old pod of previous ReplicaSet still running",
			selfName: self,
			pods: []*corev1.Pod{
				running(ownedPod(hostname, ns, self+"-newhash1", "newhash1")),
				running(ownedPod("old-pod", ns, self+"-oldhash9", "oldhash9")),
			},
			expect: 1,
		},
		{
			name:     "other applications and non-running pods are ignored",
			selfName: self,
			pods: []*corev1.Pod{
				running(ownedPod(hostname, ns, self+"-newhash1", "newhash1")),
				running(ownedPod("shop", ns, "checkout-abc12", "abc12")),
				running(ownedPod("prefix-trap", ns, self+"-extra-def34", "def34")), // Deployment "<self>-extra"
				pending(ownedPod("starting", ns, self+"-newhash1", "newhash1")),
				running(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: ns}}),
			},
			expect: 0,
		},
		{
			name:     "own Deployment unknown: error, so the caller skips the cleanup",
			selfName: "",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset()
			for _, p := range tc.pods {
				if err := client.Tracker().Add(p); err != nil {
					t.Fatalf("adding pod: %v", err)
				}
			}
			c := &KubernetesClient{KubernetesClientInterface: client, Namespace: ns, SelfDeploymentName: tc.selfName}
			got, err := c.CountOtherSafetyAutomationRunningPods(context.Background(), ns)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.expect {
				t.Errorf("count = %d, want %d", got, tc.expect)
			}
		})
	}
}

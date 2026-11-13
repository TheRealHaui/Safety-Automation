package restClient

import "k8s.io/apimachinery/pkg/runtime/schema"

// deploymentConfigGVR is the GroupVersionResource for OpenShift DeploymentConfigs,
// addressed here as a plain Kubernetes custom resource — no OpenShift client needed.
// This way the standard Kubernetes client can be used to deal with Openshift
// specific DeploymentConfigs that are not part of Kubernetes
var deploymentConfigGVR = schema.GroupVersionResource{
	Group:    "apps.openshift.io",
	Version:  "v1",
	Resource: "deploymentconfigs",
}

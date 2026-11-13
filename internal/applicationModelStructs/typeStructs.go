package applicationModelStructs

// ─── OpenShift Workload Type ───────────────────────────────────────────────────────

type OpenShiftWorkloadTypeStruct string

const (
	KindDeployment       OpenShiftWorkloadTypeStruct = "Deployment"
	KindDeploymentConfig OpenShiftWorkloadTypeStruct = "DeploymentConfig"
)

func (k OpenShiftWorkloadTypeStruct) IsValid() bool {
	switch k {
	case KindDeployment, KindDeploymentConfig:
		return true
	}
	return false
}

// ─── Test Run ─────────────────────────────────────────────────────────────────

type TestRunStatusTypeStruct string

const (
	StatusPending   TestRunStatusTypeStruct = "PENDING"
	StatusRunning   TestRunStatusTypeStruct = "RUNNING"
	StatusSuccess   TestRunStatusTypeStruct = "SUCCESS"
	StatusFailed    TestRunStatusTypeStruct = "FAILED"
	StatusCancelled TestRunStatusTypeStruct = "CANCELLED"
)

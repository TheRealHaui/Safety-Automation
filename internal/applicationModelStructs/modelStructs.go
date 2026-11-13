package applicationModelStructs

import (
	"time"
)

// ─── OpenShift Workload ───────────────────────────────────────────────────────

type Workload struct {
	Name      string
	Namespace string
	Kind      OpenShiftWorkloadTypeStruct
	// Extra detail fields (populated in detail view)
	Replicas      int32
	ReadyReplicas int32
	Image         string
	Labels        map[string]string
	CreationTime  time.Time
}

// ─── Pagination ───────────────────────────────────────────────────────────────

type PageRequest struct {
	Page     int
	PageSize int
	Search   string
	SortBy   string
	SortDir  string
}

func (p *PageRequest) Offset() int {
	if p.Page < 1 {
		p.Page = 1
	}
	return (p.Page - 1) * p.PageSize
}

type PageResult[T any] struct {
	Items      []T
	TotalCount int
	Page       int
	PageSize   int
	TotalPages int
}

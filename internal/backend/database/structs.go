package database

import (
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

//All structs representing database
//artifacts. Mostly tables but not only.
//Also structs receiving results of
//database queries should be here.

// ─── Test Scenario database table ────────────────────────────────────────────────────────────

type TestScenarioTypeStruct struct {
	ID           int64
	ScenarioName string
	GoStructName string
}

// ─── Test Run database table ─────────────────────────────────────────────────────────────────

type TestRun struct {
	ID                int64
	ScenarioTypeID    int64
	ScenarioName      string // joined
	WorkloadName      string
	WorkloadNamespace string
	WorkloadKind      applicationModelStructs.OpenShiftWorkloadTypeStruct
	StartedBy         string // username, joined
	StartedAt         time.Time
	FinishedAt        *time.Time
	Status            applicationModelStructs.TestRunStatusTypeStruct
	ResultSummary     string
	DurationSeconds   float64 // calculated
}

type TestRunURL struct {
	ID          int64
	TestRunID   int64
	URLString   string
	HTTPMethod  string
	RequestBody string // empty string means no body; only valid for POST/PUT/PATCH/DELETE
	ContentType string // only meaningful when RequestBody is non-empty
}

type TestRunLog struct {
	ID           int64
	TestRunID    int64
	LogTimestamp time.Time
	LogLevel     string
	Message      string
}

// ─── Test Run Finding database tables ────────────────────────────────────────────────────────────

type TestRunFindingType struct {
	ID              int64
	ScenarioTypeID  int64
	FindingTypeCode string
	FindingTypeName string
}

type TestRunFinding struct {
	ID              int64
	TestRunID       int64
	FindingTypeID   int64
	FindingTypeCode string // joined from TEST_RUN_FINDING_TYPE
	FindingTypeName string // joined from TEST_RUN_FINDING_TYPE
	ResultValue     string
	ResultValueType string // "string" or "number"
}

// ─── Test Run Verdict database table ─────────────────────────────────────────────────────────────

type TestRunVerdict struct {
	ID                int64
	TestRunID         int64
	VerdictName       string
	VerdictConclusion string // PASS | DEGRADED | FAIL
	VerdictText       string // human-readable explanation
	RecordedAt        time.Time
}

// ─── Test Run Phase database table ───────────────────────────────────────────────────────────────

type TestRunPhase struct {
	ID         int64
	TestRunID  int64
	PhaseLabel string
	PhaseStart time.Time
	PhaseEnd   *time.Time // nil until the phase is closed
}

// ─── Auth User database table ────────────────────────────────────────────────────────────────────

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
	IsAdmin      bool // maps to IS_ADMIN  VARCHAR2(1) 'Y'/'N' in the database
	IsEnabled    bool // maps to IS_ENABLED VARCHAR2(1) 'Y'/'N' in the database
}

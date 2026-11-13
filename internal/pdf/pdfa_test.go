package pdf

import (
	"os"
	"testing"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
)

//Test methods created by Claude to do tests
//Not used productively in any way
//To move later to test directory when there
//Maybe use with VeraPDF for verification

func TestGenerateAuditReportPDFA(t *testing.T) {
	finAt := time.Now()
	data := ReportData{
		TestRun: database.TestRun{
			ID:                1,
			WorkloadName:      "checkout",
			WorkloadNamespace: "safety-automation-shopping-demo",
			WorkloadKind:      "Deployment",
			ScenarioName:      "PodKillAndStart",
			StartedAt:         time.Now().Add(-90 * time.Second),
			FinishedAt:        &finAt,
			DurationSeconds:   90,
			StartedBy:         "developer",
			Status:            "SUCCESS",
			ResultSummary:     "Workload recovered within acceptable time.",
		},
		URLs: []database.TestRunURL{
			{URLString: "http://gateway.example.com/checkout", HTTPMethod: "POST", ContentType: "application/json", RequestBody: `{"items":[{"productId":"Prod1","quantity":1}]}`},
			{URLString: "http://gateway.example.com/health", HTTPMethod: "GET"},
		},
		Logs: []database.TestRunLog{
			{LogTimestamp: time.Now(), LogLevel: "INFO", Message: "Starting pod kill scenario"},
			{LogTimestamp: time.Now(), LogLevel: "WARN", Message: "Pod not yet ready"},
			{LogTimestamp: time.Now(), LogLevel: "INFO", Message: "Pod recovered"},
		},
		GeneratedAt: time.Now(),
	}

	b, err := GenerateAuditReport(data)
	if err != nil {
		t.Fatalf("GenerateAuditReport failed: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("empty PDF generated")
	}
	if err := os.WriteFile("/tmp/test-pdfa.pdf", b, 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	t.Logf("Written %d bytes to /tmp/test-pdfa.pdf", len(b))
}

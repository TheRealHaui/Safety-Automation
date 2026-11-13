package httpHandler

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	"github.com/TheRealHaui/Safety-Automation/internal/pdf"
	"github.com/go-chi/chi/v5"
)

// ─── PDF Download ─────────────────────────────────────────────────────────────

func (h *SharedHTTPRequestDependenciesHandlerStruct) DownloadPDF(w http.ResponseWriter, r *http.Request) {
	testRunID, err := strconv.ParseInt(chi.URLParam(r, "run_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	tr, err := database.GetTestRun(r.Context(), h.DB, testRunID)
	if err != nil {
		http.Error(w, "test run not found", http.StatusNotFound)
		return
	}
	logs, err := database.GetLogsForRun(r.Context(), h.DB, testRunID, nil)
	if err != nil {
		logs = []database.TestRunLog{}
	}
	urls, _ := database.GetTestRunURLs(r.Context(), h.DB, testRunID)
	findings, err := database.GetTestRunFindings(r.Context(), h.DB, testRunID)
	if err != nil {
		findings = []database.TestRunFinding{}
	}
	verdicts, err := database.GetTestRunVerdictsOrderedByCreationTime(r.Context(), h.DB, testRunID)
	if err != nil {
		verdicts = []database.TestRunVerdict{}
	}

	pdfBytes, err := pdf.GenerateAuditReport(pdf.ReportData{
		TestRun:     *tr,
		Logs:        logs,
		URLs:        urls,
		Findings:    findings,
		Verdicts:    verdicts,
		GeneratedAt: time.Now().UTC(),
	})
	if err != nil {
		log.Printf("pdf generation error: %v", err)
		http.Error(w, "PDF generation failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="resilience-report-RUN-%06d.pdf"`, testRunID))
	w.Header().Set("Content-Length", strconv.Itoa(len(pdfBytes)))
	w.Write(pdfBytes)
}

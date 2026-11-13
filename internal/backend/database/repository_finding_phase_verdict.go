package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SaveNonExistingFindingTypesToDatabase seeds lookup entries into TEST_RUN_FINDING_TYPE
// if they are not present yet. Called at startup — same idempotent pattern as
// SaveNonExistingScenariosToDatabase.
func SaveNonExistingFindingTypesToDatabase(ctx context.Context, db *sql.DB, types []TestRunFindingType) error {
	for _, t := range types {
		var count int
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM TEST_RUN_FINDING_TYPE WHERE FINDING_TYPE_CODE = :1`, t.FindingTypeCode).Scan(&count)
		if count == 0 {
			if _, err := db.ExecContext(ctx,
				`INSERT INTO TEST_RUN_FINDING_TYPE (SCENARIO_TYPE_ID, FINDING_TYPE_CODE, FINDING_TYPE_NAME) VALUES (:1, :2, :3)`,
				t.ScenarioTypeID, t.FindingTypeCode, t.FindingTypeName); err != nil {
				return fmt.Errorf("seeding finding type %s: %w", t.FindingTypeCode, err)
			}
		}
	}
	return nil
}

// InsertTestRunFinding looks up the finding type by code and inserts one finding row
// for the given test run. resultValue is always stored as a string — pass
// resultValueType as "number" when the value represents a numeric quantity so
// consumers can parse it correctly.
func InsertTestRunFinding(ctx context.Context, db *sql.DB, testRunID int64, findingTypeCode string, resultValue string, resultValueType string) error {
	var typeID int64
	if err := db.QueryRowContext(ctx,
		`SELECT ID FROM TEST_RUN_FINDING_TYPE WHERE FINDING_TYPE_CODE = :1`, findingTypeCode).Scan(&typeID); err != nil {
		return fmt.Errorf("looking up finding type %q: %w", findingTypeCode, err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO TEST_RUN_FINDING (TEST_RUN_ID, TEST_RUN_FINDING_TYPE_ID, RESULT_VALUE, RESULT_VALUE_TYPE) VALUES (:1, :2, :3, :4)`,
		testRunID, typeID, resultValue, resultValueType)
	return err
}

// GetTestRunFindings returns all finding rows for a test run, joined with the type
// table so callers get the code and human-readable name alongside the value.
func GetTestRunFindings(ctx context.Context, db *sql.DB, testRunID int64) ([]TestRunFinding, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT r.ID, r.TEST_RUN_ID, r.TEST_RUN_FINDING_TYPE_ID,
		        t.FINDING_TYPE_CODE, t.FINDING_TYPE_NAME,
		        r.RESULT_VALUE, r.RESULT_VALUE_TYPE
		 FROM TEST_RUN_FINDING r
		 JOIN TEST_RUN_FINDING_TYPE t ON t.ID = r.TEST_RUN_FINDING_TYPE_ID
		 WHERE r.TEST_RUN_ID = :1
		 ORDER BY r.ID ASC`, testRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TestRunFinding
	for rows.Next() {
		var r TestRunFinding
		if err := rows.Scan(&r.ID, &r.TestRunID, &r.FindingTypeID,
			&r.FindingTypeCode, &r.FindingTypeName,
			&r.ResultValue, &r.ResultValueType); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Test Run Verdict Repository ─────────────────────────────────────────────

// WriteVerdict inserts a verdict row for the given test run. Returns an error if
// the verdict name already exists for this run (ORA-00001 unique violation) — that
// signals a bug in the calling scenario code (same verdict name written twice).
// Does NOT emit an SSE log line; the caller must use the appropriate logger verdict
// method (VerdictPass / VerdictDegraded / VerdictFail) after a successful call.
func WriteVerdict(ctx context.Context, db *sql.DB, testRunID int64, verdictName string, verdictConclusion string, verdictText string) error {
	if verdictConclusion != "PASS" && verdictConclusion != "FAIL" && verdictConclusion != "DEGRADED" {
		return errors.New("invalid verdict conclusion: must be PASS, FAIL, or DEGRADED")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO TEST_RUN_VERDICT (TEST_RUN_ID, VERDICT_NAME, VERDICT_CONCLUSION, VERDICT_TEXT) VALUES (:1, :2, :3, :4)`,
		testRunID, verdictName, verdictConclusion, verdictText)
	return err
}

// GetTestRunVerdictsOrderedByCreationTime returns all verdict rows for the given run ordered by RECORDED_AT.
// Used inside CreateFinalVerdicts() to read intermediate verdicts written during Run(),
// and by the PDF handler.
func GetTestRunVerdictsOrderedByCreationTime(ctx context.Context, db *sql.DB, testRunID int64) ([]TestRunVerdict, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT ID, TEST_RUN_ID, VERDICT_NAME, VERDICT_CONCLUSION, VERDICT_TEXT, RECORDED_AT
		 FROM TEST_RUN_VERDICT
		 WHERE TEST_RUN_ID = :1
		 ORDER BY RECORDED_AT ASC`, testRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TestRunVerdict
	for rows.Next() {
		var v TestRunVerdict
		if err := rows.Scan(&v.ID, &v.TestRunID, &v.VerdictName, &v.VerdictConclusion, &v.VerdictText, &v.RecordedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// ─── Test Run Phase Repository ────────────────────────────────────────────────

// RecordPhaseStart inserts a phase row and returns its generated ID. Call this
// at the moment a scenario phase begins so the timestamp is accurate.
func RecordPhaseStart(ctx context.Context, db *sql.DB, testRunID int64, label string, start time.Time) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`INSERT INTO TEST_RUN_PHASE (TEST_RUN_ID, PHASE_LABEL, PHASE_START)
		 VALUES (:1, :2, :3) RETURNING ID INTO :4`,
		testRunID, label, start,
		sql.Named("id", sql.Out{Dest: &id}),
	).Scan(&id)
	if err != nil {
		// Fallback: godror RETURNING INTO may not work via QueryRowContext on some
		// driver versions — ExecContext + MAX(ID) as a safety net.
		_, err2 := db.ExecContext(ctx,
			`INSERT INTO TEST_RUN_PHASE (TEST_RUN_ID, PHASE_LABEL, PHASE_START) VALUES (:1, :2, :3)`,
			testRunID, label, start)
		if err2 != nil {
			return 0, fmt.Errorf("recording phase start: %w", err2)
		}
		db.QueryRowContext(ctx,
			`SELECT MAX(ID) FROM TEST_RUN_PHASE WHERE TEST_RUN_ID = :1 AND PHASE_LABEL = :2`,
			testRunID, label).Scan(&id)
	}
	return id, nil
}

// RecordPhaseEnd sets PHASE_END on an existing phase row. Call this at the moment
// the phase completes so the window is accurate for Prometheus range queries.
func RecordPhaseEnd(ctx context.Context, db *sql.DB, phaseID int64, end time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE TEST_RUN_PHASE SET PHASE_END = :1 WHERE ID = :2`, end, phaseID)
	return err
}

// GetTestRunPhases returns all phase rows for the given run ordered by PHASE_START.
// Used by Phase 2 verdict computation to determine the time windows for Prometheus
// range queries.
func GetTestRunPhases(ctx context.Context, db *sql.DB, testRunID int64) ([]TestRunPhase, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT ID, TEST_RUN_ID, PHASE_LABEL, PHASE_START, PHASE_END
		 FROM TEST_RUN_PHASE
		 WHERE TEST_RUN_ID = :1
		 ORDER BY PHASE_START ASC`, testRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TestRunPhase
	for rows.Next() {
		var p TestRunPhase
		var phaseEnd sql.NullTime
		if err := rows.Scan(&p.ID, &p.TestRunID, &p.PhaseLabel, &p.PhaseStart, &phaseEnd); err != nil {
			return nil, err
		}
		if phaseEnd.Valid {
			t := phaseEnd.Time
			p.PhaseEnd = &t
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

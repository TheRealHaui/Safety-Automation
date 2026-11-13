package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

// ─── User Repository ──────────────────────────────────────────────────────────

func GetUserByUsername(ctx context.Context, db *sql.DB, username string) (*User, error) {
	row := db.QueryRowContext(ctx,
		`SELECT ID, USERNAME, PASSWORD_HASH, CREATED_AT, LAST_LOGIN_AT, IS_ADMIN, IS_ENABLED FROM APPLICATION_USER WHERE USERNAME = :1`, username)
	return scanUser(row)
}

func GetUserByID(ctx context.Context, db *sql.DB, id int64) (*User, error) {
	row := db.QueryRowContext(ctx,
		`SELECT ID, USERNAME, PASSWORD_HASH, CREATED_AT, LAST_LOGIN_AT, IS_ADMIN, IS_ENABLED FROM APPLICATION_USER WHERE ID = :1`, id)
	return scanUser(row)
}

// scanUser scans a single APPLICATION_USER row into a User struct.
// IS_ADMIN and IS_ENABLED are stored as VARCHAR2(1) 'Y'/'N' in Oracle and mapped to bool here.
func scanUser(row rowScanner) (*User, error) {
	u := &User{}
	var lastLogin sql.NullTime
	var isAdmin, isEnabled string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &lastLogin, &isAdmin, &isEnabled); err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	u.IsAdmin = isAdmin == "Y"
	u.IsEnabled = isEnabled == "Y"
	return u, nil
}

func UpdateLastLoginAt(ctx context.Context, db *sql.DB, userID int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE APPLICATION_USER SET LAST_LOGIN_AT = SYSTIMESTAMP WHERE ID = :1`, userID)
	return err
}

func UpdateUserPassword(ctx context.Context, db *sql.DB, userID int64, newPasswordHash string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE APPLICATION_USER SET PASSWORD_HASH = :1 WHERE ID = :2`, newPasswordHash, userID)
	return err
}

func CreateUser(ctx context.Context, db *sql.DB, username, passwordHash string, isAdmin, isEnabled bool) error {
	isAdminChar := "N"
	if isAdmin {
		isAdminChar = "Y"
	}
	isEnabledChar := "N"
	if isEnabled {
		isEnabledChar = "Y"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO APPLICATION_USER (USERNAME, PASSWORD_HASH, IS_ADMIN, IS_ENABLED) VALUES (:1, :2, :3, :4)`,
		username, passwordHash, isAdminChar, isEnabledChar)
	return err
}

// ListUsers returns all users (optionally filtered by username substring).
// PASSWORD_HASH is intentionally excluded — it is never needed for display and
// keeping bcrypt hashes out of list results reduces their exposure in memory.
func ListUsers(ctx context.Context, db *sql.DB, search string) ([]User, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if search != "" {
		rows, err = db.QueryContext(ctx,
			`SELECT ID, USERNAME, CREATED_AT, LAST_LOGIN_AT, IS_ADMIN, IS_ENABLED
			 FROM APPLICATION_USER
			 WHERE UPPER(USERNAME) LIKE UPPER(:1)
			 ORDER BY USERNAME`,
			"%"+search+"%")
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT ID, USERNAME, CREATED_AT, LAST_LOGIN_AT, IS_ADMIN, IS_ENABLED
			 FROM APPLICATION_USER
			 ORDER BY USERNAME`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []User
	for rows.Next() {
		u, err := scanUserList(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *u)
	}
	return result, rows.Err()
}

// scanUserList scans a row from ListUsers (no PASSWORD_HASH column).
func scanUserList(row rowScanner) (*User, error) {
	u := &User{}
	var lastLogin sql.NullTime
	var isAdmin, isEnabled string
	if err := row.Scan(&u.ID, &u.Username, &u.CreatedAt, &lastLogin, &isAdmin, &isEnabled); err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	u.IsAdmin = isAdmin == "Y"
	u.IsEnabled = isEnabled == "Y"
	return u, nil
}

func UpdateUserFlags(ctx context.Context, db *sql.DB, userID int64, isAdmin, isEnabled bool) error {
	isAdminChar := "N"
	if isAdmin {
		isAdminChar = "Y"
	}
	isEnabledChar := "N"
	if isEnabled {
		isEnabledChar = "Y"
	}
	_, err := db.ExecContext(ctx,
		`UPDATE APPLICATION_USER SET IS_ADMIN = :1, IS_ENABLED = :2 WHERE ID = :3`,
		isAdminChar, isEnabledChar, userID)
	return err
}

// CountActiveAdmins returns the number of users who are both IS_ADMIN='Y' and IS_ENABLED='Y'.
// Used to prevent an admin from inadvertently locking everyone out by removing the last active admin.
func CountActiveAdmins(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM APPLICATION_USER WHERE IS_ADMIN = 'Y' AND IS_ENABLED = 'Y'`).Scan(&count)
	return count, err
}

func DoesAnyUserExistYet(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM APPLICATION_USER`).Scan(&count)
	return count > 0, err
}

// ─── Scenario Repository ──────────────────────────────────────────────────────

func ListScenarios(ctx context.Context, db *sql.DB) ([]TestScenarioTypeStruct, error) {
	rows, err := db.QueryContext(ctx, `SELECT ID, SCENARIO_NAME, GO_STRUCT_NAME FROM TEST_SCENARIO_TYPE ORDER BY SCENARIO_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TestScenarioTypeStruct
	for rows.Next() {
		var s TestScenarioTypeStruct
		if err := rows.Scan(&s.ID, &s.ScenarioName, &s.GoStructName); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// SaveNonExistingScenariosToDatabase Creates missing entries in the database TEST_SCENARIO_TYPE TABLE
// if missing
func SaveNonExistingScenariosToDatabase(ctx context.Context, db *sql.DB, scenarios []TestScenarioTypeStruct) error {

	//This could all together be done
	//in two single SQL statements
	//However, as this happens on application
	//start only and currently is fast okay

	for _, s := range scenarios {

		var count int
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM TEST_SCENARIO_TYPE WHERE GO_STRUCT_NAME = :1`, s.GoStructName).Scan(&count)

		if count == 0 {
			if _, err := db.ExecContext(ctx,
				`INSERT INTO TEST_SCENARIO_TYPE (SCENARIO_NAME, GO_STRUCT_NAME) VALUES (:1, :2)`,
				s.ScenarioName, s.GoStructName); err != nil {
				return fmt.Errorf("seeding scenario %s: %w", s.ScenarioName, err)
			}
		}

	}

	return nil

}

// ─── Test Run Repository ──────────────────────────────────────────────────────

func CreateTestRun(ctx context.Context, db *sql.DB, run *TestRun) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`INSERT INTO TEST_RUN (SCENARIO_TYPE_ID, WORKLOAD_NAME, WORKLOAD_NAMESPACE, WORKLOAD_KIND, STARTED_BY_APPLICATION_USER_ID, STATUS)
		 VALUES (:1, :2, :3, :4, :5, 'PENDING')
		 RETURNING ID INTO :6`,
		run.ScenarioTypeID, run.WorkloadName, run.WorkloadNamespace, string(run.WorkloadKind), run.StartedBy,
		sql.Named("id", sql.Out{Dest: &id}),
	).Scan(&id)
	if err != nil {
		// Oracle RETURNING doesn't use Scan - use ExecContext instead
		res, err2 := db.ExecContext(ctx,
			`INSERT INTO TEST_RUN (SCENARIO_TYPE_ID, WORKLOAD_NAME, WORKLOAD_NAMESPACE, WORKLOAD_KIND, STARTED_BY_APPLICATION_USER_ID, STATUS)
			 VALUES (:1, :2, :3, :4, :5, 'PENDING')`,
			run.ScenarioTypeID, run.WorkloadName, run.WorkloadNamespace, string(run.WorkloadKind), run.StartedBy)
		if err2 != nil {
			return 0, fmt.Errorf("creating test run: %w", err2)
		}
		id, _ = res.LastInsertId()
		// Oracle: fetch last inserted ID
		db.QueryRowContext(ctx, `SELECT MAX(ID) FROM TEST_RUN WHERE STARTED_BY_APPLICATION_USER_ID = :1`, run.StartedBy).Scan(&id)
	}
	return id, nil
}

func GetTestRunID(ctx context.Context, db *sql.DB, run *TestRun) (int64, error) {
	_, err := db.ExecContext(ctx,
		`INSERT INTO TEST_RUN (SCENARIO_TYPE_ID, WORKLOAD_NAME, WORKLOAD_NAMESPACE, WORKLOAD_KIND, STARTED_BY_APPLICATION_USER_ID, STATUS)
		 VALUES (:1, :2, :3, :4, :5, 'PENDING')`,
		run.ScenarioTypeID, run.WorkloadName, run.WorkloadNamespace, string(run.WorkloadKind), run.StartedBy)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRowContext(ctx,
		`SELECT MAX(ID) FROM TEST_RUN WHERE WORKLOAD_NAME = :1 AND WORKLOAD_NAMESPACE = :2 AND STARTED_BY_APPLICATION_USER_ID = :3 AND STATUS = 'PENDING'`,
		run.WorkloadName, run.WorkloadNamespace, run.StartedBy).Scan(&id)
	return id, err
}

func InsertTestRunURLs(ctx context.Context, db *sql.DB, testRunID int64, urls []TestRunURL) error {
	for i, u := range urls {
		// URL_USER_SORT stores the zero-based position of this URL as the user arranged
		// it in the web interface. It is used in GetTestRunURLs to return rows in the
		// original user-defined order, which also determines the execution order in the
		// generated K6 script.
		_, err := db.ExecContext(ctx,
			`INSERT INTO TEST_RUN_URLS_FOR_WORKLOAD (TEST_RUN_ID, URL_STRING, HTTP_METHOD_STRING, REQUEST_BODY, CONTENT_TYPE, URL_USER_SORT)
			 VALUES (:1, :2, :3, :4, :5, :6)`,
			testRunID, u.URLString, u.HTTPMethod,
			nullableString(u.RequestBody), nullableString(u.ContentType), i)
		if err != nil {
			// ORA-02290 fires when the check constraint is violated — body was supplied
			// for a method that does not permit one (not POST/PUT/PATCH/DELETE).
			// Surface a readable message rather than letting the raw Oracle error reach the user.
			if strings.Contains(err.Error(), "ORA-02290") &&
				strings.Contains(err.Error(), "TR_URL_BODY_ONLY_ALLOWED_FOR_BODY_METHODS_CHK") {
				return fmt.Errorf("request body and content-type are only permitted for POST, PUT, PATCH, and DELETE requests (method was %q)", u.HTTPMethod)
			}
			return err
		}
	}
	return nil
}

// nullableString converts an empty Go string to a NULL-compatible value for Oracle CLOB/VARCHAR2.
// Passing a plain empty string to a CLOB column works in godror but an explicit NULL is cleaner
// and makes IS NULL checks in the check constraint behave correctly.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func GetTestRunURLs(ctx context.Context, db *sql.DB, testRunID int64) ([]TestRunURL, error) {
	rows, err := db.QueryContext(ctx,
		// ORDER BY URL_USER_SORT preserves the order the user defined in the web interface.
		// Without it Oracle makes no guarantee about row return order, which would cause
		// the K6 script to execute URLs in an unpredictable sequence. Further, second order
		// column id so that maybe previously created urls before introduction of the
		// URL_USER_SORT column that all have value zero get ordered correclty too.
		// Safety fall back implementation.
		`SELECT ID, TEST_RUN_ID, URL_STRING, HTTP_METHOD_STRING, REQUEST_BODY, CONTENT_TYPE
		 FROM TEST_RUN_URLS_FOR_WORKLOAD WHERE TEST_RUN_ID = :1 ORDER BY URL_USER_SORT ASC, ID ASC`, testRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TestRunURL
	for rows.Next() {
		var u TestRunURL
		var body, ct sql.NullString
		if err := rows.Scan(&u.ID, &u.TestRunID, &u.URLString, &u.HTTPMethod, &body, &ct); err != nil {
			return nil, err
		}
		if body.Valid {
			u.RequestBody = body.String
		}
		if ct.Valid {
			u.ContentType = ct.String
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func UpdateTestRunStatus(ctx context.Context, db *sql.DB, id int64, status applicationModelStructs.TestRunStatusTypeStruct, summary string) error {
	if status == applicationModelStructs.StatusSuccess || status == applicationModelStructs.StatusFailed || status == applicationModelStructs.StatusCancelled {
		_, err := db.ExecContext(ctx,
			`UPDATE TEST_RUN SET STATUS = :1, FINISHED_AT = SYSTIMESTAMP, RESULT_SUMMARY = :2 WHERE ID = :3`,
			string(status), summary, id)
		return err
	}
	_, err := db.ExecContext(ctx,
		`UPDATE TEST_RUN SET STATUS = :1 WHERE ID = :2`, string(status), id)
	return err
}

func GetTestRun(ctx context.Context, db *sql.DB, id int64) (*TestRun, error) {
	row := db.QueryRowContext(ctx,
		`SELECT tr.ID, tr.SCENARIO_TYPE_ID, ts.SCENARIO_NAME, tr.WORKLOAD_NAME, tr.WORKLOAD_NAMESPACE,
		        tr.WORKLOAD_KIND, u.USERNAME, tr.STARTED_AT, tr.FINISHED_AT, tr.STATUS, tr.RESULT_SUMMARY
		 FROM TEST_RUN tr
		 JOIN TEST_SCENARIO_TYPE ts ON ts.ID = tr.SCENARIO_TYPE_ID
		 JOIN APPLICATION_USER u ON u.ID = tr.STARTED_BY_APPLICATION_USER_ID
		 WHERE tr.ID = :1`, id)
	return scanTestRun(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTestRun(row rowScanner) (*TestRun, error) {
	r := &TestRun{}
	var summary sql.NullString
	var finishedAt sql.NullTime
	if err := row.Scan(&r.ID, &r.ScenarioTypeID, &r.ScenarioName, &r.WorkloadName, &r.WorkloadNamespace,
		&r.WorkloadKind, &r.StartedBy, &r.StartedAt, &finishedAt, &r.Status, &summary); err != nil {
		return nil, err
	}
	if finishedAt.Valid {
		r.FinishedAt = &finishedAt.Time
		r.DurationSeconds = finishedAt.Time.Sub(r.StartedAt).Seconds()
	}
	if summary.Valid {
		r.ResultSummary = summary.String
	}
	return r, nil
}

func ListTestRuns(ctx context.Context, db *sql.DB, req applicationModelStructs.PageRequest, workloadName, workloadNamespace string) (*applicationModelStructs.PageResult[TestRun], error) {

	where := []string{}
	args := []any{}
	idx := 1

	if workloadName != "" {
		where = append(where, fmt.Sprintf("tr.WORKLOAD_NAME = :%d", idx))
		args = append(args, workloadName)
		idx++
	}
	if workloadNamespace != "" {
		where = append(where, fmt.Sprintf("tr.WORKLOAD_NAMESPACE = :%d", idx))
		args = append(args, workloadNamespace)
		idx++
	}
	if req.Search != "" {
		where = append(where, fmt.Sprintf("(UPPER(tr.WORKLOAD_NAME) LIKE UPPER(:%d) OR UPPER(ts.SCENARIO_NAME) LIKE UPPER(:%d) OR UPPER(u.USERNAME) LIKE UPPER(:%d))", idx, idx+1, idx+2))
		like := "%" + req.Search + "%"
		args = append(args, like, like, like)
		idx += 3
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	sortCol := "tr.STARTED_AT"
	switch req.SortBy {
	case "workload":
		sortCol = "tr.WORKLOAD_NAME"
	case "scenario":
		sortCol = "ts.SCENARIO_NAME"
	case "status":
		sortCol = "tr.STATUS"
	case "started_by":
		sortCol = "u.USERNAME"
	}
	sortDir := "DESC"
	if strings.ToUpper(req.SortDir) == "ASC" {
		sortDir = "ASC"
	}

	var total int
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	sqlQuery := fmt.Sprintf(`SELECT COUNT(*) FROM TEST_RUN tr
		 JOIN TEST_SCENARIO_TYPE ts ON ts.ID = tr.SCENARIO_TYPE_ID
		 JOIN APPLICATION_USER u ON u.ID = tr.STARTED_BY_APPLICATION_USER_ID %s`, whereClause)
	err := db.QueryRowContext(ctx, sqlQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, err
	}

	offset := req.Offset()
	// Order matters: OFFSET first, then FETCH NEXT.
	queryArgs := append(args, offset, req.PageSize)
	sqlQuery = fmt.Sprintf(
		`SELECT tr.ID, 
                       tr.SCENARIO_TYPE_ID, 
                       ts.SCENARIO_NAME, 
                       tr.WORKLOAD_NAME, 
                       tr.WORKLOAD_NAMESPACE,
                       tr.WORKLOAD_KIND, 
                       u.USERNAME, 
                       tr.STARTED_AT, 
                       tr.FINISHED_AT, 
                       tr.STATUS, 
                       tr.RESULT_SUMMARY
                  FROM TEST_RUN tr
                      JOIN TEST_SCENARIO_TYPE ts ON ts.ID = tr.SCENARIO_TYPE_ID
                      JOIN APPLICATION_USER u ON u.ID = tr.STARTED_BY_APPLICATION_USER_ID
                      %s
                ORDER BY %s %s
               OFFSET :%d ROWS FETCH NEXT :%d ROWS ONLY`,
		whereClause, sortCol, sortDir, idx, idx+1)
	rows, err := db.QueryContext(ctx, sqlQuery, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TestRun
	for rows.Next() {
		r, err := scanTestRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	totalPages := (total + req.PageSize - 1) / req.PageSize

	return &applicationModelStructs.PageResult[TestRun]{
		Items: items, TotalCount: total,
		Page: req.Page, PageSize: req.PageSize, TotalPages: totalPages,
	}, nil

}

// ─── Test Run Finding Repository ──────────────────────────────────────────────

// GetScenarioTypeIDByGoStructName looks up the TEST_SCENARIO_TYPE row for the given
// Go struct name and returns its ID. Used when seeding finding types that must
// reference their owning scenario.
func GetScenarioTypeIDByGoStructName(ctx context.Context, db *sql.DB, goStructName string) (int64, error) {
	var id int64
	if err := db.QueryRowContext(ctx,
		`SELECT ID FROM TEST_SCENARIO_TYPE WHERE GO_STRUCT_NAME = :1`, goStructName).Scan(&id); err != nil {
		return 0, fmt.Errorf("looking up scenario type %q: %w", goStructName, err)
	}
	return id, nil
}

// ─── Test Run Log Repository ──────────────────────────────────────────────────

// InsertLogAutonomous inserts a log entry using a separate connection to simulate autonomous transaction.
// Call this from a DB connection separate from the main transaction connection.
func InsertLog(ctx context.Context, db *sql.DB, entry TestRunLog) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO 
                      TEST_RUN_LOG (TEST_RUN_ID, LOG_TIMESTAMP, LOG_LEVEL, MESSAGE)
               VALUES (:1, :2, :3, :4)`,
		entry.TestRunID, entry.LogTimestamp, entry.LogLevel, entry.Message)
	return err
}

func GetLogsForRun(ctx context.Context, db *sql.DB, testRunID int64, since *time.Time) ([]TestRunLog, error) {
	var rows *sql.Rows
	var err error

	if since != nil {
		rows, err = db.QueryContext(ctx,
			`SELECT ID, TEST_RUN_ID, LOG_TIMESTAMP, LOG_LEVEL, MESSAGE
			 FROM TEST_RUN_LOG WHERE TEST_RUN_ID = :1 AND LOG_TIMESTAMP > :2
			 ORDER BY LOG_TIMESTAMP ASC`, testRunID, *since)
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT ID, TEST_RUN_ID, LOG_TIMESTAMP, LOG_LEVEL, MESSAGE
			 FROM TEST_RUN_LOG WHERE TEST_RUN_ID = :1
			 ORDER BY LOG_TIMESTAMP ASC`, testRunID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TestRunLog
	for rows.Next() {
		var l TestRunLog
		if err := rows.Scan(&l.ID, &l.TestRunID, &l.LogTimestamp, &l.LogLevel, &l.Message); err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	return result, rows.Err()
}

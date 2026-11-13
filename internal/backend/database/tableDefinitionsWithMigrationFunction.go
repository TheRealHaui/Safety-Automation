package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/godror/godror"
)

// Migrate creates all necessary tables if they don't exist.
func Migrate(db *sql.DB) error {
	//Note, all timestamp columns have to have time zone
	//Otherwise, Oracle would NOT use a time zone internally
	//Which could lead to uncleare situations when the client
	//runs a different time zone than the database or UTC
	stmts := []string{
		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE APPLICATION_USER (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT APPLICATION_USER_PK PRIMARY KEY,
				USERNAME VARCHAR2(100) CONSTRAINT APP_USER_USERNAME_not_null_cs NOT NULL,
				PASSWORD_HASH VARCHAR2(255) CONSTRAINT APP_USER_PASSWORD_HASH_not_null_cs NOT NULL,
				CREATED_AT TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP CONSTRAINT APP_USER_CREATED_AT_not_null_cs NOT NULL
			)';
            /* We create unique indices that way because that provides the possibility to
               set it to meaningful names which in case of error is more than desired */
            execute immediate 'create unique index application_user_username_uq_idx on application_user( username )'; 
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_SCENARIO_TYPE (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT TEST_SCENARIO_TYPE_PK PRIMARY KEY,
				SCENARIO_NAME VARCHAR2(200) CONSTRAINT TEST_SCENARIO_TYPE_SCENARIO_NAME_NOT_NULL_CST NOT NULL,
				GO_STRUCT_NAME VARCHAR2(200) CONSTRAINT TEST_SCENARIO_TYPE_GO_STRUCT_NOT_NULL_CST NOT NULL
			)';
            /* We create unique indices that way because that provides the possibility to
               set it to meaningful names which in case of error is more than desired */
            EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX TEST_SCENARIO_TYPE_NAME_UQ_IDX ON TEST_SCENARIO_TYPE(SCENARIO_NAME)';
            EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX TEST_SCENARIO_TYPE_GO_STRUCT_NAME_UNIQUE_IDX ON TEST_SCENARIO_TYPE(GO_STRUCT_NAME)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT TEST_RUN_PK PRIMARY KEY,
				SCENARIO_TYPE_ID NUMBER CONSTRAINT TEST_RUN_SCENARIO_TYPE_ID_not_null_cs NOT NULL CONSTRAINT TEST_RUN_SCENARIO_TYPE_FK REFERENCES TEST_SCENARIO_TYPE(ID),
				WORKLOAD_NAME VARCHAR2(255) CONSTRAINT TEST_RUN_WORKLOAD_NAME_not_null_cs NOT NULL,
				WORKLOAD_NAMESPACE VARCHAR2(255) CONSTRAINT TEST_RUN_WORKLOAD_NS_not_null_cs NOT NULL,
				WORKLOAD_KIND VARCHAR2(100) CONSTRAINT TEST_RUN_WORKLOAD_KIND_not_null_cs NOT NULL,
				STARTED_BY_APPLICATION_USER_ID NUMBER CONSTRAINT TEST_RUN_STARTED_BY_not_null_cs NOT NULL CONSTRAINT TEST_RUN_STARTED_BY_APPLICATION_USER_ID_FK REFERENCES APPLICATION_USER(ID),
				STARTED_AT TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP CONSTRAINT TEST_RUN_STARTED_AT_not_null_cs NOT NULL,
				FINISHED_AT TIMESTAMP WITH TIME ZONE,
				STATUS VARCHAR2(60) DEFAULT ''PENDING'' CONSTRAINT TEST_RUN_STATUS_not_null_cs NOT NULL,
				RESULT_SUMMARY CLOB  /* todo Verify if really best approach */
			)';
			/* Oracle does not create indices on foreign key columns automatically — unlike PostgreSQL.
			   Without these indices: (1) joins cause full table scans, and (2) deleting a parent row
			   (TEST_SCENARIO_TYPE or APPLICATION_USER) locks the entire TEST_RUN table. */
			EXECUTE IMMEDIATE 'CREATE INDEX test_run_scenario_type_id_idx ON TEST_RUN (SCENARIO_TYPE_ID)';
			EXECUTE IMMEDIATE 'CREATE INDEX test_run_started_by_application_user_id_idx ON TEST_RUN (STARTED_BY_APPLICATION_USER_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN_URLS_FOR_WORKLOAD (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT TEST_RUN_URL_PK PRIMARY KEY,
				TEST_RUN_ID NUMBER CONSTRAINT TR_URL_TEST_RUN_ID_not_null_cs NOT NULL CONSTRAINT TR_URL_TEST_RUN_FK REFERENCES TEST_RUN(ID),
				URL_STRING VARCHAR2(2000) CONSTRAINT TR_URL_URL_STRING_not_null_cs NOT NULL,
				HTTP_METHOD_STRING VARCHAR2(20) CONSTRAINT TR_URL_HTTP_METHOD_not_null_cs NOT NULL
			)';
			/* FK index — without it, deleting a TEST_RUN row locks the entire TEST_RUN_URLS_FOR_WORKLOAD table. */
			EXECUTE IMMEDIATE 'CREATE INDEX test_run_urls_for_workload_test_run_id_idx ON TEST_RUN_URLS_FOR_WORKLOAD (TEST_RUN_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN_LOG (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT TEST_RUN_LOG_PK PRIMARY KEY,
				TEST_RUN_ID NUMBER CONSTRAINT TR_LOG_TEST_RUN_ID_not_null_cs NOT NULL CONSTRAINT TR_LOG_TEST_RUN_FK REFERENCES TEST_RUN(ID),
				LOG_TIMESTAMP TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP CONSTRAINT TR_LOG_LOG_TIMESTAMP_not_null_cs NOT NULL,
				LOG_LEVEL VARCHAR2(20) CONSTRAINT TR_LOG_LOG_LEVEL_not_null_cs NOT NULL,
				MESSAGE CLOB
			)';
			/* FK index — TEST_RUN_LOG is the largest table at runtime; without this index a TEST_RUN
			   delete escalates to a full-table lock on all log rows, not just the affected run's rows. */
			EXECUTE IMMEDIATE 'CREATE INDEX test_run_log_test_run_id_idx ON TEST_RUN_LOG (TEST_RUN_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Lookup table for finding types — seeded at startup, extended without schema changes.
		// A code/name pair rather than a plain enum so new finding types can be added at runtime.
		// Each finding type is scoped to a specific scenario via SCENARIO_TYPE_ID, which documents
		// which scenario produces this kind of finding and enables per-scenario result queries.
		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN_FINDING_TYPE (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT TEST_RUN_FINDING_TYPE_PK PRIMARY KEY,
				SCENARIO_TYPE_ID NUMBER CONSTRAINT TRFT_SCENARIO_TYPE_ID_NOT_NULL_CS NOT NULL CONSTRAINT TRFT_SCENARIO_TYPE_FK REFERENCES TEST_SCENARIO_TYPE(ID),
				FINDING_TYPE_CODE VARCHAR2(100) CONSTRAINT TRFT_FINDING_TYPE_CODE_NOT_NULL_CS NOT NULL,
				FINDING_TYPE_NAME VARCHAR2(255) CONSTRAINT TRFT_FINDING_TYPE_NAME_NOT_NULL_CS NOT NULL
			)';
			/* Named unique indices so constraint violations produce a meaningful error message */
			EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX TRFT_FINDING_TYPE_CODE_UQ_IDX ON TEST_RUN_FINDING_TYPE (FINDING_TYPE_CODE)';
			EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX TRFT_FINDING_TYPE_NAME_UQ_IDX ON TEST_RUN_FINDING_TYPE (FINDING_TYPE_NAME)';
			/* FK index — Oracle does not auto-create these; without it deleting a TEST_SCENARIO_TYPE row
			   would lock the entire TEST_RUN_FINDING_TYPE table rather than just the affected rows. */
			EXECUTE IMMEDIATE 'CREATE INDEX TRFT_SCENARIO_TYPE_ID_IDX ON TEST_RUN_FINDING_TYPE (SCENARIO_TYPE_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Stores individual finding values for a test run — EAV style so new finding types
		// (e.g. error rate, p99 latency) can be added without schema changes.
		// result_value is always VARCHAR2 — numbers are stored as strings and interpreted
		// by the application using result_value_type ('string' or 'number').
		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN_FINDING (
				ID NUMBER GENERATED ALWAYS AS IDENTITY CONSTRAINT TEST_RUN_FINDING_PK PRIMARY KEY,
				TEST_RUN_ID NUMBER CONSTRAINT TRF_TEST_RUN_ID_NOT_NULL_CS NOT NULL CONSTRAINT TRF_TEST_RUN_FK REFERENCES TEST_RUN(ID),
				TEST_RUN_FINDING_TYPE_ID NUMBER CONSTRAINT TRF_FINDING_TYPE_ID_NOT_NULL_CS NOT NULL CONSTRAINT TRF_FINDING_TYPE_FK REFERENCES TEST_RUN_FINDING_TYPE(ID),
				RESULT_VALUE VARCHAR2(1000) CONSTRAINT TRF_RESULT_VALUE_NOT_NULL_CS NOT NULL,
				RESULT_VALUE_TYPE VARCHAR2(10) CONSTRAINT TRF_RESULT_VALUE_TYPE_NOT_NULL_CS NOT NULL,
				CONSTRAINT TRF_RESULT_VALUE_TYPE_CHK CHECK (RESULT_VALUE_TYPE IN (''string'', ''number''))
			)';
			/* Oracle does not create indices on foreign key columns automatically — unlike PostgreSQL.
			   Without these indices: (1) joins on these columns cause full table scans, and (2) deleting
			   a parent row (TEST_RUN or TEST_RUN_FINDING_TYPE) causes Oracle to lock the entire
			   TEST_RUN_FINDING table instead of just the affected rows. Both are unacceptable at scale. */
			EXECUTE IMMEDIATE 'CREATE INDEX TRF_TEST_RUN_ID_IDX ON TEST_RUN_FINDING (TEST_RUN_ID)';
			EXECUTE IMMEDIATE 'CREATE INDEX TRF_FINDING_TYPE_ID_IDX ON TEST_RUN_FINDING (TEST_RUN_FINDING_TYPE_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Adds URL_USER_SORT to TEST_RUN_URLS_FOR_WORKLOAD so that the order of URLs as
		// entered by the user in the web interface is preserved when reading them back.
		// Without an explicit ordinal, Oracle makes no promise about row return order and
		// the K6 script could execute URLs in a different sequence than the user intended.
		// DEFAULT 0 covers any rows that existed before this migration ran — those test
		// runs are already finished so the value has no effect on them.
		// ORA-01430 = "column being added already exists" — idempotency guard.
		`BEGIN
			EXECUTE IMMEDIATE 'ALTER TABLE TEST_RUN_URLS_FOR_WORKLOAD ADD (
				URL_USER_SORT NUMBER DEFAULT 0 NOT NULL
			)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -1430 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Adds LAST_LOGIN_AT to APPLICATION_USER so the profile page can show the user
		// when they last signed in. NULL means the user has never logged in since the
		// column was added (i.e. they were seeded but have not logged in yet).
		// ORA-01430 = "column being added already exists" — idempotency guard.
		`BEGIN
			EXECUTE IMMEDIATE 'ALTER TABLE APPLICATION_USER ADD (
				LAST_LOGIN_AT TIMESTAMP WITH TIME ZONE
			)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -1430 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Adds REQUEST_BODY and CONTENT_TYPE to TEST_RUN_URLS_FOR_WORKLOAD and enforces
		// that a body is only permitted for HTTP methods that semantically carry one.
		// The check constraint name is intentionally verbose so that ORA-02290 violation
		// messages in application logs and error returns identify the rule without needing
		// to look up the constraint number.
		// ORA-01430 = "column being added already exists" — used for idempotency so
		// re-running Migrate() on an already-migrated schema is safe.
		`BEGIN
			EXECUTE IMMEDIATE 'ALTER TABLE TEST_RUN_URLS_FOR_WORKLOAD ADD (
				REQUEST_BODY  CLOB,
				CONTENT_TYPE  VARCHAR2(255),
				CONSTRAINT TR_URL_BODY_ONLY_ALLOWED_FOR_BODY_METHODS_CHK CHECK (
					(REQUEST_BODY IS NULL AND CONTENT_TYPE IS NULL)
					OR UPPER(HTTP_METHOD_STRING) IN (''POST'', ''PUT'', ''PATCH'', ''DELETE'')
				)
			)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -1430 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Adds IS_ADMIN to APPLICATION_USER to distinguish admin users from regular users.
		// CHAR(1) with CHECK ('Y'/'N') is the Oracle convention for boolean flags —
		// avoids the ambiguity of NUMBER(1) and is self-documenting in SQL tools.
		// DEFAULT 'N' ensures existing rows and future inserts without an explicit value
		// are safely non-admin.
		// ORA-01430 = "column being added already exists" — idempotency guard.
		`BEGIN
			EXECUTE IMMEDIATE 'ALTER TABLE APPLICATION_USER ADD (
				IS_ADMIN varchar2(1) DEFAULT ''N''
					CONSTRAINT APP_USER_IS_ADMIN_NN_CS  NOT NULL
					CONSTRAINT APP_USER_IS_ADMIN_YN_CHK CHECK (IS_ADMIN IN (''Y'', ''N''))
			)';

		-- Ensures the pre-defined admin account always has IS_ADMIN = 'Y', regardless of
		-- whether this is an existing database (where the ALTER TABLE above just defaulted
		-- admin to 'N') or a fresh database (where the admin user does not exist yet at
		-- migration time — this UPDATE is a harmless no-op and seeding sets IS_ADMIN
		-- correctly via the INSERT). Further, it only gets executed if trying to
        -- add the IS_ADMIN column was executed successfully.
		execute immediate(' UPDATE APPLICATION_USER SET IS_ADMIN = ''Y'' WHERE USERNAME = ''admin'' ');

		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -1430 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// TEST_RUN_VERDICT stores conclusions drawn from the test — interpretations of
		// findings and Prometheus signals. Verdict names are free strings; no type table.
		// The unique index on (TEST_RUN_ID, VERDICT_NAME) prevents duplicate names per
		// run and acts as the catalog of which verdicts a run produced. New verdict types
		// are added by using new string constants in code — no schema change needed.
		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN_VERDICT (
				ID                 NUMBER GENERATED ALWAYS AS IDENTITY
				                     CONSTRAINT test_run_verdict_pk                    PRIMARY KEY,
				TEST_RUN_ID        NUMBER
				                     CONSTRAINT test_run_verdict_test_run_id_nn        NOT NULL
				                     CONSTRAINT test_run_verdict_test_run_id_fk        REFERENCES TEST_RUN(ID),
				VERDICT_NAME       VARCHAR2(200)
				                     CONSTRAINT test_run_verdict_verdict_name_nn       NOT NULL,
				VERDICT_CONCLUSION VARCHAR2(20)
				                     CONSTRAINT test_run_verdict_verdict_conclusion_nn  NOT NULL
                                     CONSTRAINT test_run_verdict_verdict_conclusion_chk CHECK (VERDICT_CONCLUSION IN (''PASS'', ''FAIL'', ''DEGRADED'')),
				VERDICT_TEXT       VARCHAR2(1000)
				                     CONSTRAINT test_run_verdict_verdict_text_nn        NOT NULL,
				RECORDED_AT        TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP
				                     CONSTRAINT test_run_verdict_recorded_at_nn         NOT NULL
			)';
			/* Unique index: each verdict name may appear at most once per run. */
			EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX test_run_verdict_run_name_uq_idx
			  ON TEST_RUN_VERDICT (TEST_RUN_ID, VERDICT_NAME)';
			/* FK index: without this, deleting a TEST_RUN row escalates to a
			   full-table lock on TEST_RUN_VERDICT. */
			EXECUTE IMMEDIATE 'CREATE INDEX test_run_verdict_test_run_id_idx
			  ON TEST_RUN_VERDICT (TEST_RUN_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// TEST_RUN_PHASE records time windows (baseline, chaos, etc.) as they happen
		// during scenario execution. Timestamps are only knowable at runtime — they
		// cannot be reconstructed after the fact. Phase records are used in Phase 2
		// verdict computation to determine the Prometheus query windows.
		// Phase labels are free strings; the unique index prevents a scenario from
		// accidentally opening the same phase twice for one run.
		`BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE TEST_RUN_PHASE (
				ID          NUMBER GENERATED ALWAYS AS IDENTITY
				              CONSTRAINT test_run_phase_pk               PRIMARY KEY,
				TEST_RUN_ID NUMBER
				              CONSTRAINT test_run_phase_test_run_id_nn   NOT NULL
				              CONSTRAINT test_run_phase_test_run_id_fk   REFERENCES TEST_RUN(ID),
				PHASE_LABEL VARCHAR2(100)
				              CONSTRAINT test_run_phase_phase_label_nn   NOT NULL,
				PHASE_START TIMESTAMP WITH TIME ZONE
				              CONSTRAINT test_run_phase_phase_start_nn   NOT NULL,
				PHASE_END   TIMESTAMP WITH TIME ZONE
			)';
			/* Unique index on (TEST_RUN_ID, PHASE_LABEL): prevents a scenario from
			   recording the same phase twice. Constants in TestScenario.go prevent
			   typos at compile time. */
			EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX test_run_phase_run_label_uq_idx
			  ON TEST_RUN_PHASE (TEST_RUN_ID, PHASE_LABEL)';
			/* FK index: without this, deleting a TEST_RUN row escalates to a
			   full-table lock on TEST_RUN_PHASE. */
			EXECUTE IMMEDIATE 'CREATE INDEX test_run_phase_test_run_id_idx
			  ON TEST_RUN_PHASE (TEST_RUN_ID)';
		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -955 THEN NULL; ELSE RAISE; END IF;
		END;`,

		// Adds IS_ENABLED to APPLICATION_USER to allow accounts to be deactivated without
		// deleting them. Same VARCHAR2(1) Y/N convention as IS_ADMIN.
		// DEFAULT 'N' — explicit enablement required; new accounts are disabled until
		// an admin activates them. The pre-defined admin account is enabled in the same
		// block so the application remains accessible after a fresh install.
		// ORA-01430 = "column being added already exists" — idempotency guard.
		`BEGIN
			EXECUTE IMMEDIATE 'ALTER TABLE APPLICATION_USER ADD (
				IS_ENABLED VARCHAR2(1) DEFAULT ''N''
					CONSTRAINT APP_USER_IS_ENABLED_NN_CS  NOT NULL
					CONSTRAINT APP_USER_IS_ENABLED_YN_CHK CHECK (IS_ENABLED IN (''Y'', ''N''))
			)';

			-- Enable the pre-defined admin account so the application is accessible after
			-- first install. On a fresh database the admin user does not exist yet at
			-- migration time, so this UPDATE is a harmless no-op; seeding sets IS_ENABLED
			-- correctly via the INSERT. Only runs when the ALTER TABLE above succeeds.
			EXECUTE IMMEDIATE 'UPDATE APPLICATION_USER SET IS_ENABLED = ''Y'' WHERE USERNAME = ''admin''';

		EXCEPTION WHEN OTHERS THEN
			IF SQLCODE = -1430 THEN NULL; ELSE RAISE; END IF;
		END;`,
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("migration error: %w\nStatement: %s", err, stmt)
		}
	}
	log.Println("Database migrations completed")
	return nil
}

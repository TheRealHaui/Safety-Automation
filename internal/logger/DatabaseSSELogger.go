package logger

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
)

// Level represents log severity.
type Level string

const (
	LevelInfo            Level = "INFO"
	LevelWarn            Level = "WARN"
	LevelError           Level = "ERROR"
	LevelDebug           Level = "DEBUG"
	LevelVerdictPass     Level = "VERDICT_PASS"
	LevelVerdictDegraded Level = "VERDICT_DEGRADED"
	LevelVerdictFail     Level = "VERDICT_FAIL"
)

// DatabaseSSELogger is the resilience-test-run-scoped logger.
// It writes each log entry to the DB using a dedicated connection (autonomous semantics)
// and also sends entries to an in-memory channel for SSE streaming.
type DatabaseSSELogger struct {
	testRunID int64
	logDB     *sql.DB // separate DB connection for autonomous log writes
	stream    chan database.TestRunLog
}

// New creates a new DatabaseSSELogger for the given test run.
// logDB should be a separate *sql.DB connection (not a shared pool) to achieve
// autonomous transaction semantics – log entries are committed immediately
// regardless of what happens to the main transaction.
func New(testRunID int64, logDB *sql.DB, stream chan database.TestRunLog) *DatabaseSSELogger {
	return &DatabaseSSELogger{
		testRunID: testRunID,
		logDB:     logDB,
		stream:    stream,
	}
}

func (l *DatabaseSSELogger) log(level Level, msg string) {
	entry := database.TestRunLog{
		TestRunID:    l.testRunID,
		LogTimestamp: time.Now().UTC(),
		LogLevel:     string(level),
		Message:      msg,
	}
	// Write to DB immediately (autonomous)
	if l.logDB != nil {
		_ = database.InsertLog(context.Background(), l.logDB, entry)
	}
	// Send to SSE stream (non-blocking)
	if l.stream != nil {
		select {
		case l.stream <- entry:
		default:
		}
	}
}

func (l *DatabaseSSELogger) Info(format string, args ...any) {
	l.log(LevelInfo, fmt.Sprintf(format, args...))
}

func (l *DatabaseSSELogger) Warn(format string, args ...any) {
	l.log(LevelWarn, fmt.Sprintf(format, args...))
}

func (l *DatabaseSSELogger) Error(format string, args ...any) {
	l.log(LevelError, fmt.Sprintf(format, args...))
}

func (l *DatabaseSSELogger) Debug(format string, args ...any) {
	l.log(LevelDebug, fmt.Sprintf(format, args...))
}

func (l *DatabaseSSELogger) VerdictPass(format string, args ...any) {
	l.log(LevelVerdictPass, fmt.Sprintf(format, args...))
}

func (l *DatabaseSSELogger) VerdictDegraded(format string, args ...any) {
	l.log(LevelVerdictDegraded, fmt.Sprintf(format, args...))
}

func (l *DatabaseSSELogger) VerdictFail(format string, args ...any) {
	l.log(LevelVerdictFail, fmt.Sprintf(format, args...))
}

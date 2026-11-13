package database

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/godror/godror"
)

// Open opens the Oracle DB connection using godror DSN.
// Example DSN: user/password@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=host)(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=svc)))
// For wallet: set TNS_ADMIN env var to wallet directory, then DSN can be: user/password@service_name
func Open(dsn string) (*sql.DB, error) {

	db, err := sql.Open("godror", dsn)

	if err != nil {
		return nil, fmt.Errorf("opening oracle db: %w", err)
	}

	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("pinging oracle db: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)

	return db, nil

}

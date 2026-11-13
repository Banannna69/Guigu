package server

import (
	"database/sql"
	"os"
	"testing"

	"artex/db"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// server test suite so cross-package DELETE cleanup races with db/agent
// packages are avoided when running `go test ./...`.
// TestMain isolates the server test suite from a shared PostgreSQL in three ways:
//
//  1. An advisory lock (7337741002) serialises this package against the db/agent
//     packages when the whole tree runs under `go test ./...`.
//  2. GOMAXPROCS=1 for the package's own tests: every Server here shares one
//     database, and the pre_agent tests assert on "only my task is running".
//     Independent goroutines from different tests interleave otherwise.
//  3. ARTEX_LOAD_ONLY_TASK defaults to a value that matches no row, so a freshly
//     constructed Manager does not adopt the hundreds of task rows earlier runs
//     left behind (its scheduler/engine would then drive them). Tests that are
//     specifically about restoring set the variable themselves via t.Setenv.
func TestMain(m *testing.M) {
	if os.Getenv("ARTEX_LOAD_ONLY_TASK") == "" {
		_ = os.Setenv("ARTEX_LOAD_ONLY_TASK", "no-such-task-row")
	}

	dsn, _, err := db.DSN()
	if err != nil {
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}

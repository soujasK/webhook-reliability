package store

import (
	"database/sql"
	"embed"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func Connect(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// RunMigrations applies every embedded .sql file. Statements use CREATE ... IF NOT EXISTS,
// so this is safe to run on every startup.
func RunMigrations(db *sql.DB) error {
	const migrationLockKey int64 = 2024061201

	if _, err := db.Exec("SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = db.Exec("SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		sqlBytes, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL_UNPOOLED"))
	if dsn == "" {
		log.Fatal("DATABASE_URL_UNPOOLED is required to run schema migrations")
	}
	migrations := []struct {
		path string
		name string
	}{
		{path: "migrations/001_protected_rooms.sql", name: "001_protected_rooms.sql"},
		{path: "migrations/002_room_host_controls.sql", name: "002_room_host_controls.sql"},
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal("open migration database: invalid PostgreSQL configuration")
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal("connect to migration database: connection failed; check the direct database URL")
	}
	for _, migration := range migrations {
		content, err := os.ReadFile(migration.path)
		if err != nil {
			log.Fatalf("read migration %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, string(content)); err != nil {
			log.Fatalf("apply migration %s: failed; check database permissions and migration state", migration.name)
		}
		log.Printf("Applied migration %s", migration.name)
	}
}

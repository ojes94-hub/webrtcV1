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
	migration, err := os.ReadFile("migrations/001_protected_rooms.sql")
	if err != nil {
		log.Fatalf("read protected-room migration: %v", err)
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
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		log.Fatal("apply protected-room migration: failed; check database permissions and migration state")
	}
	log.Println("Applied migration 001_protected_rooms.sql")
}

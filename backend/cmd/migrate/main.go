package main

import (
	"fmt"
	"os"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: loading config: %v\n", err)
		os.Exit(1)
	}

	dbh, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: connecting to database (%s): %v\n", cfg.DatabaseURL, err)
		os.Exit(1)
	}

	if err := db.Migrate(dbh); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: applying migrations: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Migrations applied successfully.")
}

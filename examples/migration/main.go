// Package main demonstrates how to run strego migrations for PostgreSQL.
//
// This example shows different ways to apply the required database schema:
//   - Programmatically using the migrations package
//   - Printing SQL for manual execution
//   - Using with external migration tools (golang-migrate, goose, etc.)
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"

	"github.com/erennakbas/strego/migrations"
)

func main() {
	// Command line flags
	dsn := flag.String("dsn", "", "PostgreSQL connection string (e.g., postgres://user:pass@localhost:5432/dbname?sslmode=disable)")
	printSQL := flag.Bool("print", false, "Print migration SQL without executing")
	listFiles := flag.Bool("list", false, "List available migration files")
	flag.Parse()

	// List available migrations
	if *listFiles {
		files, err := migrations.ListMigrations()
		if err != nil {
			log.Fatalf("Failed to list migrations: %v", err)
		}
		fmt.Println("Available migration files:")
		for _, f := range files {
			fmt.Printf("  - %s\n", f)
		}
		return
	}

	// Get all migration SQL
	sql, err := migrations.GetAllSQL()
	if err != nil {
		log.Fatalf("Failed to get migration SQL: %v", err)
	}

	// Print SQL only
	if *printSQL {
		fmt.Println("-- Strego PostgreSQL Migration SQL")
		fmt.Println("-- Run this SQL to create the required tables and indexes")
		fmt.Println()
		fmt.Println(sql)
		return
	}

	// Execute migration
	if *dsn == "" {
		fmt.Println("Usage:")
		fmt.Println("  # Print SQL (for manual execution or external tools)")
		fmt.Println("  go run main.go -print")
		fmt.Println()
		fmt.Println("  # List migration files")
		fmt.Println("  go run main.go -list")
		fmt.Println()
		fmt.Println("  # Run migration against database")
		fmt.Println("  go run main.go -dsn 'postgres://user:pass@localhost:5432/mydb?sslmode=disable'")
		fmt.Println()
		fmt.Println("For production, consider using a migration tool like golang-migrate or goose.")
		os.Exit(0)
	}

	if err := runMigration(*dsn, sql); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	fmt.Println("Migration completed successfully!")
}

func runMigration(dsn, migrationSQL string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	ctx := context.Background()

	// Execute migration in a transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	if _, err := tx.ExecContext(ctx, migrationSQL); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to execute migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// Package migrations provides embedded SQL migration files for strego.
package migrations

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.sql
var migrationFS embed.FS

// GetAllSQL returns all migration SQL files concatenated in order.
// Files are sorted by name (001_, 002_, etc.) to ensure correct order.
func GetAllSQL() (string, error) {
	entries, err := migrationFS.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	var builder strings.Builder
	for _, file := range files {
		data, err := migrationFS.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("failed to read migration file %s: %w", file, err)
		}
		builder.WriteString(string(data))
		builder.WriteString("\n\n")
	}

	return builder.String(), nil
}

// GetMigrationSQL returns a specific migration file's SQL content.
func GetMigrationSQL(filename string) (string, error) {
	data, err := migrationFS.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("failed to read migration file %s: %w", filename, err)
	}
	return string(data), nil
}

// ListMigrations returns a list of available migration files.
func ListMigrations() ([]string, error) {
	entries, err := migrationFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

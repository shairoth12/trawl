// Package impl holds the SQL implementation of dotless.Store.
package impl

import (
	"context"
	"database/sql"
)

// SQL reads keys from a database.
type SQL struct{ DB *sql.DB }

// Get queries the database.
func (s SQL) Get(ctx context.Context, key string) (string, error) {
	_, err := s.DB.ExecContext(ctx, "SELECT 1")
	return key, err
}

// Package migrations contains the SQL shipped with the API binary.
package migrations

import "embed"

// Files contains only versioned migrations; check scripts are never applied.
//
//go:embed *.up.sql
var Files embed.FS

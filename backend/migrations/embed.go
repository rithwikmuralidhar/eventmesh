// Package migrations embeds the SQL migration files so the migrate binary
// needs no files next to it and no separate CLI install.
package migrations

import "embed"

// FS holds every *.sql migration.
//
//go:embed *.sql
var FS embed.FS

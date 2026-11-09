// Package analytics holds the ANALYTICAL database's schema (ADR 0013): the
// golang-migrate files under migrations/, embedded so cmd/os-projector applies
// them without a migrations directory in the image (the same approach as the
// OLTP migrations in internal/adapters/outbound/postgres). The analytical
// database is separate from the OLTP one; it is written only by
// cmd/os-projector and read (read-only) by cmd/os-reports.
package analytics

import "embed"

// Migrations is the analytical schema: migrations/*.{up,down}.sql.
//
//go:embed migrations/*.sql
var Migrations embed.FS

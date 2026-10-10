package migrations

import _ "embed"

// InitialSQL is the unmodified three-table schema from the governing design.
//
//go:embed 001_initial.sql
var InitialSQL string

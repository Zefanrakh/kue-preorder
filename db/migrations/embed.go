// Package migrations embeds the goose SQL migrations, so tests and the deploy
// step apply exactly the files in this directory.
//
// Migrations are forward-only once merged: add a new file, never edit one.
package migrations

import "embed"

// FS holds every migration file in this directory.
//
//go:embed *.sql
var FS embed.FS

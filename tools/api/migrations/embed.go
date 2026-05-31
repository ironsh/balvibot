// Package migrations embeds the Goose SQL migrations so they ship inside the
// binary and can be applied by `api migrate up` or any subcommand at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

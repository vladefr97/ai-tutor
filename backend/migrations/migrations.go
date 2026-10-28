// Package migrations embeds the goose SQL migrations applied at server start.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

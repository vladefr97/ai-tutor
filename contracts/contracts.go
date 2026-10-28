// Package contracts embeds the schemas shared by the Go backend and the React client.
package contracts

import "embed"

//go:embed *.json *.yaml examples/course/valid
var FS embed.FS

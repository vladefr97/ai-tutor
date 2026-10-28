// Package contracts embeds the schemas shared by the Go backend and the React client.
package contracts

import "embed"

//go:embed *.json *.yaml
var FS embed.FS

// Package web holds the HTML templates and static files, built into the binary.
package web

import "embed"

//go:embed templates static
var FS embed.FS

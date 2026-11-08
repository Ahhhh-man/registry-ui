//go:build !dev

package ui

import "embed"

// A release build reads templates and assets only from the binary.
//
//go:embed static templates
var files embed.FS

const dev = false

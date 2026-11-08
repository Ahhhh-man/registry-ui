//go:build dev

package ui

import "os"

// Relative to the repository root, where task dev runs.
var files = os.DirFS("internal/ui")

const dev = true

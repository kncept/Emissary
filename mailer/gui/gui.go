package gui

import "embed"

// Files contains the built frontend assets.
//
//go:embed all:frontend/dist
var Files embed.FS

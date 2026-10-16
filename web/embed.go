// Package web contains the browser assets served by ts.
package web

import "embed"

// Assets contains the complete, locally served browser interface.
//
//go:embed static/*
var Assets embed.FS

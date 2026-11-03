// Package web holds the parent app and embeds it into the binary.
//
// Embedding means deploying is one static file with no asset directory to mount, get out
// of sync or forget in a backup. The whole app is ~40 KB of hand-written HTML, CSS and
// JavaScript with no build step and no framework — on a box whose job is to stay up for
// years, a toolchain that needs maintaining is a liability.
package web

import "embed"

//go:embed index.html app.js styles.css manifest.webmanifest sw.js icon.svg
var Assets embed.FS

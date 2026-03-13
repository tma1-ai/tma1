// Package web embeds the static dashboard files.
package web

import "embed"

//go:embed *.html *.svg css js vendor
var FS embed.FS

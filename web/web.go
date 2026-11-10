// Package web embeds the Mapper browser UI (HTML, JavaScript, CSS,
// images, and vendored libraries) so the server binary serves it
// without depending on the working directory at runtime.
package web

import "embed"

// FS holds the browser UI assets rooted at this directory.
//
//go:embed index.html login.html logs.html unlock.html *.js *.css img vendor
var FS embed.FS

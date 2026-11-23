// Package webui carries the web UI's static assets, compiled into the binary.
//
// They are embedded rather than read from disk because where a binary runs
// from must not decide whether it can serve its own interface. Serving them
// from the relative path "internal/webui/static" meant the UI worked only when
// the process's working directory happened to be a source checkout: a clean
// install put the binaries in /usr/local/bin with no assets shipped at all and
// no WorkingDirectory in the unit, so every non-API route returned 404 while
// /api/* kept answering. That is the shape that passes a post-deploy smoke
// test and fails for the operator, and it survived precisely because nobody
// opened the page a person opens.
//
// Embedding also means scripts/deploy.sh has no asset payload to get right,
// and no host needs a source tree left behind for the UI to work.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
)

// landingPage is the one file the UI cannot start without. Everything else the
// browser asks for is requested by this page.
const landingPage = "index.html"

// AssetError reports embedded assets that cannot serve the interface.
//
// It exists so that a broken embed is a startup failure with a name, rather
// than a process that comes up healthy and answers 404 to every page while
// /api/* keeps working -- which is the exact production failure embedding was
// meant to end, and would be an unimprovement if the embed could break the
// same way.
type AssetError struct {
	Path   string
	Reason string
	Err    error
}

func (e *AssetError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("webui: embedded asset %s %s: %v", e.Path, e.Reason, e.Err)
	}
	return fmt.Sprintf("webui: embedded asset %s %s", e.Path, e.Reason)
}

func (e *AssetError) Unwrap() error { return e.Err }

// staticAssets holds internal/webui/static as built into the binary.
//
// The "all:" prefix includes files whose names begin with "." or "_", which
// the default pattern silently skips. Nothing in the tree needs it today; it
// is there so that adding one later cannot drop a file out of the binary
// without anyone noticing.
//
//go:embed all:static
var staticAssets embed.FS

// Static returns the static asset tree rooted at its top level, so that
// index.html is "index.html" rather than "static/index.html".
//
// It fails if the tree cannot serve the UI. fs.Sub alone is not enough of a
// check: it succeeds as long as the directory exists, so a tree that had lost
// index.html -- a bad merge, a rename, a build that assembled the wrong
// directory -- would start cleanly and 404 the landing page. That is the
// failure this package exists to prevent, so it is checked here rather than
// left to be discovered by whoever opens the page.
func Static() (fs.FS, error) {
	assets, err := fs.Sub(staticAssets, "static")
	if err != nil {
		return nil, &AssetError{Path: "static", Reason: "is not a usable directory", Err: err}
	}

	if err := validateAssets(assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// validateAssets reports whether a rooted asset tree can serve the UI.
//
// It takes an fs.FS rather than reading the embedded tree directly so the
// check itself is testable against a tree that is deliberately broken; there
// is no way to build one out of a go:embed directive.
func validateAssets(assets fs.FS) error {
	info, err := fs.Stat(assets, landingPage)
	if err != nil {
		return &AssetError{Path: landingPage, Reason: "is missing", Err: err}
	}
	if info.Size() == 0 {
		return &AssetError{Path: landingPage, Reason: "is empty"}
	}
	return nil
}

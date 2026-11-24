// Package webui carries the web UI's static assets, embedded so the UI works from any working directory.
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

// staticAssets holds internal/webui/static; "all:" includes names starting with "." or "_".
//
//go:embed all:static
var staticAssets embed.FS

// Static returns the asset tree rooted at its top level, failing if it cannot serve the UI.
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

package webui

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAppJS runs the front-end tests in testdata/app_js_test.js against
// static/js/app.js under node. The script prints one ok/FAIL line per case.
func TestAppJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the app.js tests cannot run")
	}

	script := filepath.Join("testdata", "app_js_test.js")
	app := filepath.Join("static", "js", "app.js")

	out, err := exec.Command(node, script, app).CombinedOutput()
	if err != nil {
		t.Fatalf("app.js tests failed: %v\n%s", err, out)
	}
}

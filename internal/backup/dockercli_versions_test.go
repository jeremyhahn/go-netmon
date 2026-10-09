package backup

import (
	"context"
	"testing"
)

// Empty-but-successful output is not an answer: Versions keeps "unknown" and
// ImageID keeps its documented "" fallback.
func TestVersionsKeepsUnknownWithoutAnAnswer(t *testing.T) {
	cases := []struct {
		name string
		fake *fakeExec
		want Versions
	}{
		{"empty", &fakeExec{stdout: map[string]string{"Server.Version": "", "--short": ""}},
			Versions{Docker: "unknown", Compose: "unknown"}},
		{"whitespace", &fakeExec{stdout: map[string]string{"Server.Version": " \n", "--short": "\t\n"}},
			Versions{Docker: "unknown", Compose: "unknown"}},
		{"real values trimmed", &fakeExec{stdout: map[string]string{"Server.Version": "26.1.0\n", "--short": " 2.27.0\n"}},
			Versions{Docker: "26.1.0", Compose: "2.27.0"}},
		{"errors", &fakeExec{fail: map[string]string{"Server.Version": "boom", "--short": "boom"}},
			Versions{Docker: "unknown", Compose: "unknown"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cliWith(t, tc.fake).Versions(context.Background())
			if err != nil {
				t.Fatalf("Versions: %v", err)
			}
			if got != tc.want {
				t.Errorf("Versions = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestImageIDKeepsFallbackWithoutAnAnswer(t *testing.T) {
	cases := []struct {
		name string
		fake *fakeExec
		want string
	}{
		{"empty", &fakeExec{stdout: map[string]string{"image inspect": ""}}, ""},
		{"whitespace", &fakeExec{stdout: map[string]string{"image inspect": "  \n"}}, ""},
		{"real value trimmed", &fakeExec{stdout: map[string]string{"image inspect": "sha256:abc\n"}}, "sha256:abc"},
		{"error", &fakeExec{fail: map[string]string{"image inspect": "no such image"}}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cliWith(t, tc.fake).ImageID(context.Background(), "img:1")
			if err != nil {
				t.Fatalf("ImageID: %v", err)
			}
			if got != tc.want {
				t.Errorf("ImageID = %q, want %q", got, tc.want)
			}
		})
	}
}

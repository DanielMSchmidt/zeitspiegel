package main

import (
	"strings"
	"testing"
)

// UT-35: every card in the field is byte-identical except for its label, so
// "which build is this one running?" is a question only the binary can answer.
// A stamped build answers verbatim; an unstamped one says so rather than
// inventing a version, because a wrong version in a bug report is worse than
// an admitted unknown.
func TestBuildVersion(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	for _, tc := range []struct {
		name    string
		stamped string
		want    string
	}{
		{"release tag", "v1.4.2", "v1.4.2"},
		{"git describe with distance and dirt", "v1.4.2-3-gabc1234-dirty", "v1.4.2-3-gabc1234-dirty"},
		{"whitespace trimmed — it reaches a log line and a filename", "  v1.4.2\n", "v1.4.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version = tc.stamped
			if got := buildVersion(); got != tc.want {
				t.Errorf("buildVersion() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("unstamped build never returns empty", func(t *testing.T) {
		version = ""
		got := buildVersion()
		if got == "" {
			t.Fatal("an unstamped build reports an empty version, which reads as a missing field")
		}
		// Either the VCS stamp the toolchain embeds, or an honest admission.
		if strings.ContainsAny(got, " \t\n") {
			t.Errorf("version %q has whitespace in it; it ends up in log lines and filenames", got)
		}
	})
}

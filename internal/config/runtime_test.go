package config_test

import (
	"errors"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

func ptr[T any](v T) *T { return &v }

// Runtime config + PATCH semantics (REQUIREMENTS §3, FR-9).
func TestRuntimeWithPatch(t *testing.T) {
	r := config.Default().Runtime()
	if r.Profile != "720p60" || !r.MirrorFlip {
		t.Fatalf("runtime from defaults = %+v", r)
	}

	r2, err := r.WithPatch(config.Patch{Profile: ptr("1080p30"), MirrorFlip: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Profile != "1080p30" || r2.MirrorFlip {
		t.Errorf("patched = %+v", r2)
	}
	if r2.BufferMaxS != r.BufferMaxS {
		t.Error("unpatched field changed")
	}
	if r.Profile != "720p60" {
		t.Error("WithPatch mutated the receiver")
	}

	if _, err := r.WithPatch(config.Patch{Profile: ptr("4k120")}); !errors.Is(err, config.ErrInvalid) {
		t.Errorf("bad profile: err = %v, want ErrInvalid", err)
	}
	if _, err := r.WithPatch(config.Patch{BufferMaxS: ptr(-1.0)}); !errors.Is(err, config.ErrInvalid) {
		t.Errorf("negative buffer: err = %v, want ErrInvalid", err)
	}
}

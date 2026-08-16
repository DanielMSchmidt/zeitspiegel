package config

// Runtime settings outlive the process they were changed in (FR-18).
//
// A guest turns the mirroring off on the unit in front of them, somebody
// unplugs the installation at the end of the evening, and the next morning the
// mirror is back the way it was — which is not what "changed the setting"
// means to anyone. So every accepted PATCH is written to a small file and
// applied over the boot config at the next start.
//
// Two decisions are baked into the shape of that file:
//
//   - It is a **patch, not a snapshot**: only the keys somebody actually set.
//     deploy/config.toml keeps governing everything nobody has touched, so
//     editing a default on the card still changes what that unit boots with. A
//     stored full config would freeze every default at the moment of the first
//     PATCH and silently mask later ones.
//
//   - It belongs on the **FAT32 boot partition** (`state_file`), next to the
//     unit's name file. The appliance's root is a read-only overlay (NFR-9),
//     so a file written anywhere else lands in tmpfs and is gone at exactly
//     the power cut this exists to survive — and the boot partition is the one
//     place an operator can also read or delete it with the card in a reader.
//
// FAT has no journal and the unit is switched off by pulling its plug, so the
// write goes to a scratch file that is fsynced and renamed into place: a power
// cut costs the new value, never the file (NFR-9). Writes happen only when a
// value actually changes, not on a schedule — a mirror must not be writing to
// its boot partition while somebody dances in front of it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// LoadOverrides reads the stored patch. A missing file is the ordinary first
// boot and yields an empty patch; an empty path means persistence is off.
// A file that cannot be parsed is an error naming it — the caller boots on the
// config file's values instead, because a stray byte on the boot partition
// must not cost the venue its mirror.
func LoadOverrides(path string) (Patch, error) {
	if path == "" {
		return Patch{}, nil
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Patch{}, nil
	case err != nil:
		return Patch{}, fmt.Errorf("settings %s: %w", path, err)
	}
	var p Patch
	if err := json.Unmarshal(b, &p); err != nil {
		return Patch{}, fmt.Errorf("settings %s: %w", path, err)
	}
	return p, nil
}

// SaveOverrides writes the patch atomically. An empty path is a no-op: that is
// a development run on a laptop, where there is no boot partition to write to
// and a failure per PATCH would be noise.
func SaveOverrides(path string, p Patch) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	b = append(b, '\n') // the card ends up in a reader eventually

	// Same directory as the target: a rename is only atomic within one
	// filesystem, and this one is the boot partition.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no scratch file left on the partition the Pi boots from

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("settings %s: %w", path, err)
	}
	// The bytes have to be on the card before the rename, or a power cut can
	// leave the new name pointing at an empty file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("settings %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	return nil
}

// Merge returns p with q's set fields laid over it; neither is modified. The
// stored patch is everything ever set on this unit, so a later profile change
// must not drop an earlier mirror flip.
func (p Patch) Merge(q Patch) Patch {
	if q.MirrorFlip != nil {
		p.MirrorFlip = q.MirrorFlip
	}
	if q.Profile != nil {
		p.Profile = q.Profile
	}
	if q.BufferMaxS != nil {
		p.BufferMaxS = q.BufferMaxS
	}
	if q.FocusAuto != nil {
		p.FocusAuto = q.FocusAuto
	}
	if q.FocusAbsolute != nil {
		p.FocusAbsolute = q.FocusAbsolute
	}
	if q.ExposureAuto != nil {
		p.ExposureAuto = q.ExposureAuto
	}
	if q.ExposureAbsolute != nil {
		p.ExposureAbsolute = q.ExposureAbsolute
	}
	return p
}

// WithRuntime folds an effective runtime back into the boot config, leaving
// every key outside the runtime subset alone. Startup uses it so that
// everything reading the config — the ring buffer's budget, the camera open,
// the display's flip — sees what the unit is actually running under rather
// than what the file booted with.
func (c Config) WithRuntime(r Runtime) Config {
	c.MirrorFlip = r.MirrorFlip
	c.Profile = r.Profile
	c.BufferMaxS = r.BufferMaxS
	c.FocusAuto = r.FocusAuto
	c.FocusAbsolute = r.FocusAbsolute
	c.ExposureAuto = r.ExposureAuto
	c.ExposureAbsolute = r.ExposureAbsolute
	return c
}

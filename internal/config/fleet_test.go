package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

func writeFleet(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// UT-20: a config that says nothing about the fleet must behave exactly like
// a single appliance did before multi-unit support existed (E-7) — one unit,
// no election, no radio management.
func TestFleetDefaults(t *testing.T) {
	c := config.Default()
	if c.FleetSize != 1 {
		t.Errorf("FleetSize = %d, want 1", c.FleetSize)
	}
	if c.NetworkManage {
		t.Error("NetworkManage = true, want false — a dev build must never touch the radio")
	}
	if c.NameFile != "" {
		t.Errorf("NameFile = %q, want empty (identity picks the default path)", c.NameFile)
	}
}

// UT-20: the fleet keys parse, and they are the same on every card.
func TestFleetLoad(t *testing.T) {
	c, err := config.Load(writeFleet(t, `
fleet_size = 3
network_manage = true
name_file = "/boot/firmware/zeitspiegel-name.txt"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.FleetSize != 3 {
		t.Errorf("FleetSize = %d, want 3", c.FleetSize)
	}
	if !c.NetworkManage {
		t.Error("NetworkManage = false, want true")
	}
	if c.NameFile != "/boot/firmware/zeitspiegel-name.txt" {
		t.Errorf("NameFile = %q", c.NameFile)
	}
}

// UT-20: a nonsensical fleet size is a startup error with a clear message,
// not a unit that silently elects itself into a corner.
func TestFleetValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		toml string
		want string
	}{
		{"zero", "fleet_size = 0", "fleet_size"},
		{"negative", "fleet_size = -1", "fleet_size"},
		{"absurd", "fleet_size = 999", "fleet_size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(writeFleet(t, tc.toml))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// UT-20: the production config is what actually ships, so pin the fleet
// contract on it the way UT-17 pins the buffer capacity.
func TestDeployConfigManagesTheRadio(t *testing.T) {
	c, err := config.Load("../../deploy/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.NetworkManage {
		t.Error("deploy/config.toml must manage the radio, or no unit ever elects a role")
	}
	if c.FleetSize < 1 {
		t.Errorf("FleetSize = %d", c.FleetSize)
	}
}

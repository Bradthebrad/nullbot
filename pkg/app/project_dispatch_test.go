package app

import (
	"testing"
)

func TestProjectValidationDoesNotMutateLivePermissions(t *testing.T) {
	a := New(projectTestConfig(t, "read-only"))
	err := a.UpdateConfig(func(c *Config) { c.Projects[0].Permission = "invalid" })
	if err == nil {
		t.Fatal("invalid grant saved")
	}
	if a.Config().Projects[0].Permission != "read-only" {
		t.Fatal("failed update mutated live permission")
	}
}

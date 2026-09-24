package config_test

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// TestProfilesDeclarationIsACentralDirectoryMatchingTheProfileStore ties the
// declared profiles/ row to the directory internal/core/profile stores
// profile files in, and holds it in the central layer. That package owns the
// storage (and imports this one), so the name is held equal here rather than
// shared.
func TestProfilesDeclarationIsACentralDirectoryMatchingTheProfileStore(t *testing.T) {
	if profile.DirName != config.ProfilesDirName {
		t.Fatalf("profile.DirName = %q, config.ProfilesDirName = %q; the declared profiles/ row names the wrong directory", profile.DirName, config.ProfilesDirName)
	}
	item, ok := config.LookupSetting(profile.DirName + "/")
	if !ok || item.Layer != config.LayerCentral || item.Shape != config.SettingDir {
		t.Fatalf("profiles/ declaration = %+v, %v; want a central directory", item, ok)
	}
}

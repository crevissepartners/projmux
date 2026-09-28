package config_test

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// TestProjectLinksDeclarationMatchesTheProjectLinksStore ties the declared
// project-links/ row to the directory internal/core/projectlinks actually
// stores rule files in. That package owns the storage (and imports this one),
// so the name is held equal here rather than shared.
func TestProjectLinksDeclarationMatchesTheProjectLinksStore(t *testing.T) {
	if projectlinks.DirName != config.ProjectLinksDirName {
		t.Fatalf("projectlinks.DirName = %q, config.ProjectLinksDirName = %q; the declared project-links/ row names the wrong directory", projectlinks.DirName, config.ProjectLinksDirName)
	}
	item, ok := config.LookupSetting(projectlinks.DirName + "/")
	if !ok || item.Layer != config.LayerCentral || item.Shape != config.SettingDir {
		t.Fatalf("project-links/ declaration = %+v, %v; want a central directory", item, ok)
	}
}

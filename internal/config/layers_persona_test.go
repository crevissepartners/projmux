package config_test

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// TestPersonasDeclarationMatchesThePersonaStore ties the declared personas/
// row to the directory internal/core/persona actually stores persona files
// in. That package owns the storage (and imports this one), so the name is
// held equal here rather than shared.
func TestPersonasDeclarationMatchesThePersonaStore(t *testing.T) {
	if persona.DirName != config.PersonasDirName {
		t.Fatalf("persona.DirName = %q, config.PersonasDirName = %q; the declared personas/ row names the wrong directory", persona.DirName, config.PersonasDirName)
	}
	item, ok := config.LookupSetting(persona.DirName + "/")
	if !ok || item.Layer != config.LayerCentral || item.Shape != config.SettingDir {
		t.Fatalf("personas/ declaration = %+v, %v; want a central directory", item, ok)
	}
}

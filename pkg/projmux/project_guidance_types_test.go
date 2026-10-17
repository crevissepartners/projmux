package projmux_test

import (
	"reflect"
	"testing"

	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProjectGuidancePublicFieldCoverage(t *testing.T) {
	for _, pair := range [][2]reflect.Type{
		{reflect.TypeFor[guidance.ProjectFile](), reflect.TypeFor[core.ProjectGuidanceFile]()},
		{reflect.TypeFor[guidance.ProjectValidation](), reflect.TypeFor[core.ProjectGuidanceValidation]()},
	} {
		compareResourceShape(t, pair[0], pair[1], map[[2]reflect.Type]bool{})
	}
}

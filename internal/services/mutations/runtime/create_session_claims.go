package runtime

import (
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func ResourceSessionProjectClaims(registry coremetadata.Registry, sessionName string, reconciler *RegistryReconciler) []string {
	var claims []string
	for _, project := range registry.Projects {
		name := ""
		if project.Status.Session != nil {
			name = strings.TrimSpace(project.Status.Session.Name)
		}
		if name == "" && reconciler.SessionNameFor != nil {
			name = strings.TrimSpace(reconciler.SessionNameFor(project.Spec.Root))
		}
		if name == sessionName {
			claims = append(claims, project.Metadata.UID)
		}
	}
	slices.Sort(claims)
	return claims
}

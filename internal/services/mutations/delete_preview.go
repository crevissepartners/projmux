package mutations

import coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"

// DeletePreview is the transport-independent projection of a planned removal.
type DeleteResource struct {
	Kind coremetadata.Kind `json:"kind"`
	UID  string            `json:"uid"`
	Name string            `json:"name"`
}
type DeletePreviewTarget struct {
	DeleteResource
	Descendants []DeleteResource `json:"descendants"`
}
type DeletePreview struct {
	Kind     coremetadata.Kind     `json:"kind"`
	Targets  []DeletePreviewTarget `json:"targets"`
	Cascades int                   `json:"cascades"`
	Affected []DeletionAffected    `json:"affected"`
}

func PreviewDelete(plan DeletePlan) DeletePreview {
	p := DeletePreview{Kind: plan.Kind, Targets: []DeletePreviewTarget{}, Cascades: plan.Cascades(), Affected: []DeletionAffected{}}
	for _, target := range plan.Targets {
		t := DeletePreviewTarget{DeleteResource: DeleteResource{Kind: plan.Kind, UID: target.Match.UID, Name: target.Match.Name}, Descendants: []DeleteResource{}}
		p.Affected = append(p.Affected, DeletionAffected{Kind: string(plan.Kind), UID: t.UID, Name: t.Name, Action: DeletionAffectedDeleted})
		for _, d := range target.Descendants {
			t.Descendants = append(t.Descendants, DeleteResource(d))
			p.Affected = append(p.Affected, DeletionAffected{Kind: string(d.Kind), UID: d.UID, Name: d.Name, Action: DeletionAffectedDeleted})
		}
		p.Targets = append(p.Targets, t)
	}
	return p
}

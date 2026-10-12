package runtime

func (p DeletePlan) Uids() []string {
	out := make([]string, 0, len(p.Targets))
	for _, target := range p.Targets {
		out = append(out, target.Match.UID)
	}
	return out
}

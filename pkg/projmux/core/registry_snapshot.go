package core

// RegistrySnapshot contains stored public resources from one Registry read.
// Rows retain their stored order, including duplicate UIDs and Offline Agents.
// Each slice is non-nil on success and its complete object graph belongs to the
// caller. No runtime observation or resolved ownership is added; a Pane's
// runtime kind is its stored Spec.Runtime, read with EffectiveKind.
type RegistrySnapshot struct {
	Projects []Project `json:"projects"`
	Windows  []Window  `json:"windows"`
	Panes    []Pane    `json:"panes"`
	Agents   []Agent   `json:"agents"`
}

package core

// RegistrySnapshot contains stored public resources from one Registry read.
// Rows retain their stored order, including duplicate UIDs and Offline Agents.
// Each slice is non-nil on success and its complete object graph belongs to the
// caller. No runtime observation or resolved ownership is added.
type RegistrySnapshot struct {
	Projects []Project `json:"projects"`
	Windows  []Window  `json:"windows"`
	Agents   []Agent   `json:"agents"`
}

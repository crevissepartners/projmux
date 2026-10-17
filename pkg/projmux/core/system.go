package core

// SystemSample contains the statusbar's host-scoped CPU and memory percentages.
// Supported describes platform support, independently of sample availability.
// Nil percentages mean unavailable; non-nil values are integers from 0 to 100.
// Each result and its pointers belong to the caller.
type SystemSample struct {
	Supported     bool `json:"supported"`
	CPUPercent    *int `json:"cpu_percent"`
	MemoryPercent *int `json:"memory_percent"`
}

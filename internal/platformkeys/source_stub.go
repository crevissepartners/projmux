//go:build !darwin || !cgo

package platformkeys

import "context"

// Available reports whether this build contains the native physical-key
// adapter. Non-Darwin builds deliberately keep the existing terminal path.
func Available() bool {
	return false
}

// NewSource returns a no-op source on platforms without the native adapter.
func NewSource() Source {
	return stubSource{}
}

type stubSource struct{}

func (stubSource) Replace([]Binding) error   { return nil }
func (stubSource) SetEnabled(bool)           {}
func (stubSource) Ready() <-chan struct{}    { return closedReady }
func (stubSource) Events() <-chan string     { return nil }
func (stubSource) Run(context.Context) error { return nil }

var closedReady = func() <-chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}()

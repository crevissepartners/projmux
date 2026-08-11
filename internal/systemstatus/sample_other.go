//go:build !linux && (!darwin || !cgo)

package systemstatus

func (s Sampler) Sample() Metrics { return Metrics{} }

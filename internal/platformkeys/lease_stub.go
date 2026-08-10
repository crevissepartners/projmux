//go:build !darwin

package platformkeys

// AcquireLease is never used by the non-Darwin app path.
func AcquireLease(string) (func(), bool, error) {
	return func() {}, false, nil
}

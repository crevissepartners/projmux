package app

// newRuntimeRouteIdentityCache opens a cache for exactly one transaction. A
// blank operation id yields no cache at all, which keeps every guard on the
// uncached path.
func newRuntimeRouteIdentityCache(operationID string) *runtimeRouteIdentityCache {
	if operationID == "" {
		return nil
	}
	return &runtimeRouteIdentityCache{OperationID: operationID, Open: true, Proved: map[runtimeRouteIdentityKey]struct{}{}}
}

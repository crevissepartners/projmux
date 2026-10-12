package runtime

func NewRuntimeRouteIdentityCache(operationID string) *RuntimeRouteIdentityCache {
	if operationID == "" {
		return nil
	}
	return &RuntimeRouteIdentityCache{OperationID: operationID, Open: true, Proved: map[RuntimeRouteIdentityKey]struct{}{}}
}

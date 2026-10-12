package runtime

// bindExactDeleteTarget pins a delete seam to one resolved server and drops
// every expectation the previous binding derived from the old one.
func BindExactDeleteTarget(target TmuxTransport, bound *TmuxTransport,
	expectedSocketPath, expectedLogicalSocket *string, routeAuthority **RuntimeMutationRouteAuthority) {
	*bound = target
	*expectedSocketPath = ""
	*expectedLogicalSocket = ""
	*routeAuthority = nil
}

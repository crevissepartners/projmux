package core

// ProviderAccount is a non-sensitive local directory reference. AuthenticationStatus
// is always unverified; registration never authenticates or starts a provider.
type ProviderAccount struct {
	UID                  string `json:"uid"`
	Name                 string `json:"name"`
	Provider             string `json:"provider"`
	Root                 string `json:"root"`
	Version              int    `json:"version"`
	State                string `json:"state"`
	AuthenticationStatus string `json:"authenticationStatus"`
}

// RegisterAccountRequest registers an existing absolute directory without reading its contents.
type RegisterAccountRequest struct{ Name, Provider, Root string }

// RemoveAccountRequest retires an exact active name or uid:account-… reference.
type RemoveAccountRequest struct{ Ref string }

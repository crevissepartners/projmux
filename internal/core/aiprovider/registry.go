package aiprovider

import (
	"sort"
	"strings"
)

type ID string

const (
	Claude      ID = "claude"
	Codex       ID = "codex"
	Antigravity ID = "antigravity"
)

type Metadata struct {
	ID           ID
	DisplayName  string
	ShortName    string
	BinaryName   string
	UsageModel   string
	HookProvider string

	SettingsVisible bool
	PickerEligible  bool
	UsageSupported  bool
	Integrate       SupportMetadata
	HookDiagnostics SupportMetadata
	SessionState    SupportMetadata

	SettingsOrder int
	PickerOrder   int
}

type SupportMetadata struct {
	Supported bool
	ID        string
	Name      string
	Command   string
}

var registry = []Metadata{
	{
		ID:              Claude,
		DisplayName:     "Claude",
		ShortName:       "Claude",
		BinaryName:      "claude",
		UsageModel:      string(Claude),
		HookProvider:    string(Claude),
		SettingsVisible: true,
		PickerEligible:  true,
		UsageSupported:  true,
		Integrate: SupportMetadata{
			Supported: true,
			Command:   "projmux ai integrate claude",
		},
		HookDiagnostics: SupportMetadata{
			Supported: true,
			ID:        "claude-hooks",
			Name:      "Claude Code hooks",
		},
		SessionState:  SupportMetadata{Supported: true},
		SettingsOrder: 10,
		PickerOrder:   20,
	},
	{
		ID:              Codex,
		DisplayName:     "Codex",
		ShortName:       "Codex",
		BinaryName:      "codex",
		UsageModel:      string(Codex),
		HookProvider:    string(Codex),
		SettingsVisible: true,
		PickerEligible:  true,
		UsageSupported:  true,
		Integrate: SupportMetadata{
			Supported: true,
			Command:   "projmux ai integrate codex",
		},
		HookDiagnostics: SupportMetadata{
			Supported: true,
			ID:        "codex-hooks",
			Name:      "Codex hooks",
		},
		SessionState:  SupportMetadata{Supported: true},
		SettingsOrder: 20,
		PickerOrder:   10,
	},
	{
		ID:              Antigravity,
		DisplayName:     "Antigravity",
		ShortName:       "Antigravity",
		BinaryName:      "agy",
		SettingsVisible: true,
		PickerEligible:  true,
		SettingsOrder:   30,
		PickerOrder:     30,
	},
}

func All() []Metadata {
	return append([]Metadata(nil), registry...)
}

func Lookup(id string) (Metadata, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, provider := range registry {
		if string(provider.ID) == id {
			return provider, true
		}
	}
	return Metadata{}, false
}

func SettingsVisible() []Metadata {
	out := filter(func(provider Metadata) bool {
		return provider.SettingsVisible
	})
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SettingsOrder < out[j].SettingsOrder
	})
	return out
}

func PickerEligible() []Metadata {
	out := filter(func(provider Metadata) bool {
		return provider.PickerEligible
	})
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PickerOrder < out[j].PickerOrder
	})
	return out
}

func UsageSupported() []Metadata {
	out := filter(func(provider Metadata) bool {
		return provider.UsageSupported && strings.TrimSpace(provider.UsageModel) != ""
	})
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SettingsOrder < out[j].SettingsOrder
	})
	return out
}

func HookDiagnosticSupported() []Metadata {
	out := filter(func(provider Metadata) bool {
		return provider.HookDiagnostics.Supported
	})
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SettingsOrder < out[j].SettingsOrder
	})
	return out
}

func SessionStateSupported(id string) bool {
	provider, ok := Lookup(id)
	return ok && provider.SessionState.Supported
}

func filter(keep func(Metadata) bool) []Metadata {
	out := []Metadata{}
	for _, provider := range registry {
		if keep(provider) {
			out = append(out, provider)
		}
	}
	return out
}

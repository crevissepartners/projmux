// Package metadata defines the Projmux resource metadata model: the
// Project -> Window -> (Pane | Agent) ownership tree, its opaque identity
// (`metadata.uid`), its stable query key (`metadata.name`), and the atomic
// name-suffix allocator that backs both.
//
// The package is pure. It performs no file, tmux, or process I/O; every
// environment-dependent input (clock, uid source, root-directory existence)
// arrives through the injected Mutator. Persistence and the tmux transport
// mirror live in internal/integrations/metadata.
//
// Identity boundary: tmux raw identity (`$N`, `@N`, `%N`, `pane_title`,
// `window_name`) is adapter/status transport and is deliberately absent from
// this model. tmux ids are resolved to a uid by the adapter, never stored as
// identity here.
//
// Field spelling in the on-disk registry follows the resource-model contract
// (`apiVersion`, `schemaVersion`, `metadata`, `displayName`, `ownerRef`,
// `primaryPaneRef`, `spec`, `status`) rather than the snake_case spelling used
// by the older projmux state files. See docs/architecture.md.
package metadata

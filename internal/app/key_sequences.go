package app
import ("github.com/crevissepartners/projmux/internal/app/keybinding"; "github.com/crevissepartners/projmux/internal/services/tmuxconfig")
func keySequenceTableName(prefix []string) string {
	return tmuxconfig.KeySequenceTableName(prefix)
}
func keySequenceGeneratedState(actions []keybinding.KeyBindingAction) (roots, tables []string) {
	return tmuxconfig.KeySequenceGeneratedState(actions)
}
func tmuxSequenceStateLines(actions []keybinding.KeyBindingAction) []string {
	return tmuxconfig.TmuxSequenceStateLines(actions)
}
func keySequenceRetireCommandsWithPrefix(prefix []string, roots, tables string) [][]string {
	return tmuxconfig.KeySequenceRetireCommandsWithPrefix(prefix, roots, tables)
}
func tmuxSequenceBindLines(binaryPath string, actions []keybinding.KeyBindingAction) []string {
	return tmuxconfig.TmuxSequenceBindLines(binaryPath, actions)
}
func keyBindingSequenceTransportChords(actions []keybinding.KeyBindingAction) []string {
	return tmuxconfig.KeyBindingSequenceTransportChords(actions)
}
const (tmuxSequenceTablesOption = tmuxconfig.TmuxSequenceTablesOption; tmuxSequenceRootsOption = tmuxconfig.TmuxSequenceRootsOption; tmuxSequenceTablePrefix = tmuxconfig.TmuxSequenceTablePrefix)

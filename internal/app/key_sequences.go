package app

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/platformkeys"
)

const (
	tmuxSequenceTablesOption = "@projmux_sequence_tables"
	tmuxSequenceRootsOption  = "@projmux_sequence_roots"
	tmuxSequenceTablePrefix  = "projmux-sequence-"
)

type keySequenceTrieNode struct {
	prefix   []string
	children map[string]*keySequenceTrieNode
	action   *keyBindingAction
}

func compileKeySequenceTrie(actions []keyBindingAction) *keySequenceTrieNode {
	root := &keySequenceTrieNode{children: map[string]*keySequenceTrieNode{}}
	type entry struct {
		sequence string
		action   keyBindingAction
	}
	var entries []entry
	for _, action := range actions {
		for _, sequence := range keyBindingEffectiveSequences(action) {
			entries = append(entries, entry{sequence: sequence, action: action})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].sequence != entries[j].sequence {
			return entries[i].sequence < entries[j].sequence
		}
		return entries[i].action.ID < entries[j].action.ID
	})
	for i := range entries {
		node := root
		for stroke := range strings.SplitSeq(entries[i].sequence, " ") {
			if node.children[stroke] == nil {
				prefix := append(append([]string(nil), node.prefix...), stroke)
				node.children[stroke] = &keySequenceTrieNode{prefix: prefix, children: map[string]*keySequenceTrieNode{}}
			}
			node = node.children[stroke]
		}
		action := entries[i].action
		node.action = &action
	}
	return root
}

func keySequenceTableName(prefix []string) string {
	sum := sha256.Sum256([]byte(strings.Join(prefix, "\x00")))
	return tmuxSequenceTablePrefix + hex.EncodeToString(sum[:8])
}

func sortedSequenceChildStrokes(node *keySequenceTrieNode) []string {
	strokes := make([]string, 0, len(node.children))
	for stroke := range node.children {
		strokes = append(strokes, stroke)
	}
	sort.Strings(strokes)
	return strokes
}

func keySequenceGeneratedState(actions []keyBindingAction) (roots, tables []string) {
	trie := compileKeySequenceTrie(actions)
	roots = sortedSequenceChildStrokes(trie)
	var walk func(*keySequenceTrieNode)
	walk = func(node *keySequenceTrieNode) {
		for _, stroke := range sortedSequenceChildStrokes(node) {
			child := node.children[stroke]
			if len(child.children) != 0 {
				tables = append(tables, keySequenceTableName(child.prefix))
				walk(child)
			}
		}
	}
	walk(trie)
	sort.Strings(tables)
	return roots, tables
}

// tmuxSequenceCleanupLines retires exactly the generated roots and tables
// recorded by the previous successful source. run-shell is synchronous here;
// the new single-chord and sequence bindings are rendered afterwards.
func tmuxSequenceCleanupLines(actions []keyBindingAction) []string {
	roots, tables := keySequenceGeneratedState(actions)
	socket := tmuxShellQuote("#{socket_path}")
	rootCleanup := `for key in $(tmux -S ` + socket + ` show-option -gqv ` + tmuxSequenceRootsOption + `); do tmux -S ` + socket + ` unbind-key -q -n "$key"; done`
	tableCleanup := `for table in $(tmux -S ` + socket + ` show-option -gqv ` + tmuxSequenceTablesOption + `); do tmux -S ` + socket + ` unbind-key -a -q -T "$table"; done`
	return []string{
		"run-shell " + tmuxConfigQuote(rootCleanup),
		"run-shell " + tmuxConfigQuote(tableCleanup),
		"set-option -g " + tmuxSequenceRootsOption + " " + tmuxConfigQuote(strings.Join(roots, " ")),
		"set-option -g " + tmuxSequenceTablesOption + " " + tmuxConfigQuote(strings.Join(tables, " ")),
	}
}

func tmuxSequenceBindLines(binaryPath string, actions []keyBindingAction) []string {
	trie := compileKeySequenceTrie(actions)
	var lines []string
	for _, stroke := range sortedSequenceChildStrokes(trie) {
		child := trie.children[stroke]
		lines = append(lines, "bind-key -n "+stroke+" switch-client -T "+keySequenceTableName(child.prefix))
	}
	var walk func(*keySequenceTrieNode)
	walk = func(node *keySequenceTrieNode) {
		if len(node.children) == 0 {
			return
		}
		table := keySequenceTableName(node.prefix)
		lines = append(lines,
			"bind-key -T "+table+" Escape switch-client -T root",
			"bind-key -T "+table+" Any switch-client -T root",
		)
		for _, stroke := range sortedSequenceChildStrokes(node) {
			child := node.children[stroke]
			if child.action != nil {
				lines = append(lines, "bind-key -T "+table+" "+stroke+" "+renderTmuxBindingBody(binaryPath, *child.action))
			} else {
				lines = append(lines, "bind-key -T "+table+" "+stroke+" switch-client -T "+keySequenceTableName(child.prefix))
			}
			walk(child)
		}
	}
	for _, stroke := range sortedSequenceChildStrokes(trie) {
		walk(trie.children[stroke])
	}
	return lines
}

// keyBindingSequenceTransportChords is transport metadata, not action
// dispatch. Only strokes representable by the native physical adapter enter
// the broker allowlist; ordinary later strokes continue through the terminal.
func keyBindingSequenceTransportChords(actions []keyBindingAction) []string {
	var chords []string
	for _, action := range actions {
		for _, sequence := range keyBindingEffectiveSequences(action) {
			for stroke := range strings.SplitSeq(sequence, " ") {
				if _, ok := platformkeys.ParseBinding(stroke); ok {
					chords = append(chords, stroke)
				}
			}
		}
	}
	return uniqueNonEmptyStrings(chords)
}

package tmuxconfig

import (
	"crypto/sha256"
	"encoding/hex"
	platformkeys "github.com/crevissepartners/projmux/internal/platformkeys/binding"
	"github.com/crevissepartners/projmux/internal/services/keybinding"
	"sort"
	"strings"
)

func compileKeySequenceTrie(actions []keybinding.KeyBindingAction) *keySequenceTrieNode {
	root := &keySequenceTrieNode{children: map[string]*keySequenceTrieNode{}}
	type entry struct {
		sequence string
		action   keybinding.KeyBindingAction
	}
	var entries []entry
	for _, action := range actions {
		for _, sequence := range keybinding.KeyBindingEffectiveSequences(action) {
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

func KeySequenceTableName(prefix []string) string {
	sum := sha256.Sum256([]byte(strings.Join(prefix, "\x00")))
	return TmuxSequenceTablePrefix + hex.EncodeToString(sum[:8])
}

func sortedSequenceChildStrokes(node *keySequenceTrieNode) []string {
	strokes := make([]string, 0, len(node.children))
	for stroke := range node.children {
		strokes = append(strokes, stroke)
	}
	sort.Strings(strokes)
	return strokes
}

func KeySequenceGeneratedState(actions []keybinding.KeyBindingAction) (roots, tables []string) {
	trie := compileKeySequenceTrie(actions)
	roots = sortedSequenceChildStrokes(trie)
	var walk func(*keySequenceTrieNode)
	walk = func(node *keySequenceTrieNode) {
		for _, stroke := range sortedSequenceChildStrokes(node) {
			child := node.children[stroke]
			if len(child.children) != 0 {
				tables = append(tables, KeySequenceTableName(child.prefix))
				walk(child)
			}
		}
	}
	walk(trie)
	sort.Strings(tables)
	return roots, tables
}

func TmuxSequenceStateLines(actions []keybinding.KeyBindingAction) []string {
	roots, tables := KeySequenceGeneratedState(actions)
	return []string{
		"set-option -g " + TmuxSequenceRootsOption + " " + keybinding.TmuxConfigQuote(strings.Join(roots, " ")),
		"set-option -g " + TmuxSequenceTablesOption + " " + keybinding.TmuxConfigQuote(strings.Join(tables, " ")),
	}
}

func KeySequenceRetireCommandsWithPrefix(prefix []string, roots, tables string) [][]string {
	var commands [][]string
	for key := range strings.FieldsSeq(roots) {
		command := append(append([]string(nil), prefix...), "unbind-key", "-q", "-n", key)
		commands = append(commands, command)
	}
	for table := range strings.FieldsSeq(tables) {
		command := append(append([]string(nil), prefix...), "unbind-key", "-a", "-q", "-T", table)
		commands = append(commands, command)
	}
	return commands
}

func TmuxSequenceBindLines(binaryPath string, actions []keybinding.KeyBindingAction) []string {
	trie := compileKeySequenceTrie(actions)
	var lines []string
	for _, stroke := range sortedSequenceChildStrokes(trie) {
		child := trie.children[stroke]
		lines = append(lines, "bind-key -n "+stroke+" switch-client -T "+KeySequenceTableName(child.prefix))
	}
	var walk func(*keySequenceTrieNode)
	walk = func(node *keySequenceTrieNode) {
		if len(node.children) == 0 {
			return
		}
		table := KeySequenceTableName(node.prefix)
		lines = append(lines,
			"bind-key -T "+table+" Escape switch-client -T root",
			"bind-key -T "+table+" Any switch-client -T root",
		)
		for _, stroke := range sortedSequenceChildStrokes(node) {
			child := node.children[stroke]
			if child.action != nil {
				lines = append(lines, "bind-key -T "+table+" "+stroke+" "+keybinding.RenderTmuxBindingBody(binaryPath, *child.action))
			} else {
				lines = append(lines, "bind-key -T "+table+" "+stroke+" switch-client -T "+KeySequenceTableName(child.prefix))
			}
			walk(child)
		}
	}
	for _, stroke := range sortedSequenceChildStrokes(trie) {
		walk(trie.children[stroke])
	}
	return lines
}

func KeyBindingSequenceTransportChords(actions []keybinding.KeyBindingAction) []string {
	var chords []string
	for _, action := range actions {
		for _, sequence := range keybinding.KeyBindingEffectiveSequences(action) {
			for stroke := range strings.SplitSeq(sequence, " ") {
				if _, ok := platformkeys.ParseBinding(stroke); ok {
					chords = append(chords, stroke)
				}
			}
		}
	}
	return keybinding.UniqueNonEmptyStrings(chords)
}

const (
	TmuxSequenceTablesOption = "@projmux_sequence_tables"
	TmuxSequenceRootsOption  = "@projmux_sequence_roots"
	TmuxSequenceTablePrefix  = "projmux-sequence-"
)

type keySequenceTrieNode struct {
	prefix   []string
	children map[string]*keySequenceTrieNode
	action   *keybinding.KeyBindingAction
}

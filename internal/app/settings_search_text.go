package app

import (
	"math/bits"
	"slices"
	"strings"
	"unicode"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// settingsSearchKeyDelimiter separates a builder SearchKey from the rendered
// label columns joined around it. Labels and builder keys carry no tab, and a
// typed query never does, so the delimiter only marks the boundary.
const settingsSearchKeyDelimiter = "\t"

// withSettingsRenderedLabelSearchText joins each keyed row's rendered label
// onto its SearchKey. The native picker matches a row's SearchText alone
// whenever it is set, so a hand-written (mostly English) key would otherwise
// hide the localized label the user actually sees.
//
// The label is joined column by column (glyph, name, description: the runs
// split by the two-space column gap), in label order, as
// "before\tkey\tafter" ("key\tafter" when nothing goes before):
//
//   - An option name is a row's rendered name column, that name in lowercase,
//     or the last one or two words of its builder SearchKey (the config key and
//     value, such as "split_cwd_from pane"). A row guards one of its option
//     names when it is the first row the picker keeps for that name, matching
//     keyed rows either by their builder SearchKey alone or by their name
//     column + " " + SearchKey, and rows without a SearchKey by their picker
//     text.
//   - A column goes after the key unless the row's joined text would then
//     match an option name a later row guards; it goes before the key under
//     the same check. A column that fits on neither side is placed word by
//     word the same way, and a word that fits nowhere is left out. Only a
//     later row's name counts: the picker keeps input order, so only an
//     earlier row can take Enter.
//
// So typing an option name never moves Enter off the row it already selected,
// and a shared description (both split start options say "Pane directory")
// cannot hand a sibling's name to the row above it. The builder key is kept
// verbatim between the delimiters, so every key match still matches. Rows
// without a SearchKey, keys that already contain the label (the Settings root
// rows) and keys that were already joined stay untouched, which keeps
// SearchKey-free Views in the picker's scored mode and makes the join
// idempotent. Matching follows picker.FilterItems exactly, so the guard uses
// the picker's smart-case, scored subsequence rule.
//
// Keyed Views whose rows come from user data (Pinned Projects, discovery
// roots, hook events) are rendered through this on every render, so rows are
// indexed by the runes their texts hold and a name is only tried against rows
// holding every rune it needs, which keeps the cost close to linear.
func withSettingsRenderedLabelSearchText(options intpickercompat.Options) intpickercompat.Options {
	entries := options.Entries
	firstJoin := slices.IndexFunc(entries, settingsSearchJoinable)
	if firstJoin < 0 {
		return options
	}

	joinable := make([]bool, len(entries))
	for i := firstJoin; i < len(entries); i++ {
		joinable[i] = settingsSearchJoinable(entries[i])
	}
	index := newSettingsSearchIndex(entries, firstJoin, joinable)
	copied := false
	for i := firstJoin; i < len(entries); i++ {
		if !joinable[i] {
			continue
		}
		joined, ok := index.join(i)
		if !ok {
			continue
		}
		if !copied {
			entries = slices.Clone(entries)
			copied = true
		}
		entries[i].SearchKey = joined
	}
	options.Entries = entries
	return options
}

// settingsSearchJoinable reports a keyed row whose key neither contains its
// rendered label nor was already joined.
func settingsSearchJoinable(entry intpickercompat.Entry) bool {
	if strings.TrimSpace(entry.SearchKey) == "" || strings.Contains(entry.SearchKey, settingsSearchKeyDelimiter) {
		return false
	}
	label := strings.TrimSpace(stripSettingsLabelANSI(entry.Label))
	return label != "" && !strings.Contains(entry.SearchKey, label)
}

// settingsSearchBuilderKey returns the builder SearchKey inside a joined key.
func settingsSearchBuilderKey(key string) string {
	switch fields := strings.Split(key, settingsSearchKeyDelimiter); len(fields) {
	case 2:
		return fields[0]
	case 3:
		return fields[1]
	}
	return key
}

type settingsSearchRow struct {
	key, label string
	// names are the option names this row can guard.
	names []settingsSearchQuery
	// texts are what the picker matches before the join: the builder key alone
	// and the name column + " " + key for a keyed row, the picker text twice
	// for a row without a SearchKey.
	texts [2]settingsSearchText
}

func newSettingsSearchRow(entry intpickercompat.Entry) settingsSearchRow {
	label := strings.TrimSpace(stripSettingsLabelANSI(entry.Label))
	row := settingsSearchRow{key: settingsSearchBuilderKey(entry.SearchKey), label: label}
	var names []string
	name := settingsSearchNameColumn(label)
	if name != "" {
		names = append(names, name, strings.ToLower(name))
	}
	if strings.TrimSpace(row.key) == "" {
		text := newSettingsSearchText(intpicker.Item{Label: entry.Label, Value: entry.Value}.EffectiveSearchText())
		row.texts = [2]settingsSearchText{text, text}
	} else {
		row.texts = [2]settingsSearchText{newSettingsSearchText(row.key), newSettingsSearchText(name + " " + row.key)}
		if words := strings.Fields(row.key); len(words) > 0 {
			names = append(names, words[len(words)-1])
			if len(words) > 1 {
				names = append(names, words[len(words)-2]+" "+words[len(words)-1])
			}
		}
	}
	for _, value := range names {
		query := newSettingsSearchQuery(value)
		if len(query.pattern) > 0 && !slices.ContainsFunc(row.names, func(existing settingsSearchQuery) bool { return existing.value == query.value }) {
			row.names = append(row.names, query)
		}
	}
	return row
}

// settingsSearchIndex holds one View's pre-join texts and the option names
// its rows guard.
type settingsSearchIndex struct {
	rows []settingsSearchRow
	// guards lists each guarded option name once, with the last row guarding
	// it, ordered from the last row back.
	guards []settingsSearchGuard
	firsts map[settingsSearchFirstKey]*settingsSearchFirst
	// holders[t][case] indexes the rows by the rune bits of pre-join text t,
	// folded (0) or as written (1).
	holders [2][2]settingsSearchRowBits
	// relevant lists, per joinable row and in guard order, the guarded names of
	// later rows that are a subsequence of the row's key with its whole label
	// on both sides. Every placement is a subsequence of that text, so no other
	// guarded name can match the joined row.
	relevant [][]settingsSearchQuery
}

type settingsSearchGuard struct {
	name settingsSearchQuery
	row  int
}

type settingsSearchFirstKey struct {
	text int
	name string
}

// settingsSearchFirst is a resumable scan for the first row whose pre-join
// text matches a name, over the rows holding every rune bit of the name.
type settingsSearchFirst struct {
	row, scanned int
	candidates   []uint64
}

func newSettingsSearchIndex(entries []intpickercompat.Entry, firstJoin int, joinable []bool) *settingsSearchIndex {
	words := (len(entries) + 63) / 64
	index := &settingsSearchIndex{
		rows:     make([]settingsSearchRow, len(entries)),
		firsts:   map[settingsSearchFirstKey]*settingsSearchFirst{},
		relevant: make([][]settingsSearchQuery, len(entries)),
	}
	for i, entry := range entries {
		index.rows[i] = newSettingsSearchRow(entry)
		for t := range index.rows[i].texts {
			text := &index.rows[i].texts[t]
			index.holders[t][0].add(i, words, text.foldedMask)
			index.holders[t][1].add(i, words, text.rawMask)
		}
	}
	// Rows at or before the first joinable row cannot be overtaken, so only
	// later rows guard their names.
	at := map[string]int{}
	for r := firstJoin + 1; r < len(index.rows); r++ {
		for n := range index.rows[r].names {
			name := &index.rows[r].names[n]
			if !index.guarded(r, name) {
				continue
			}
			if i, ok := at[name.value]; ok {
				index.guards[i].row = r
				continue
			}
			at[name.value] = len(index.guards)
			index.guards = append(index.guards, settingsSearchGuard{name: *name, row: r})
		}
	}
	slices.SortStableFunc(index.guards, func(a, b settingsSearchGuard) int { return b.row - a.row })

	wholes := make([]settingsSearchText, len(entries))
	var wholeHolders [2]settingsSearchRowBits
	for e := firstJoin; e < len(entries); e++ {
		if !joinable[e] {
			continue
		}
		row := &index.rows[e]
		wholes[e] = newSettingsSearchText(row.label + settingsSearchKeyDelimiter + row.key + settingsSearchKeyDelimiter + row.label)
		wholeHolders[0].add(e, words, wholes[e].foldedMask)
		wholeHolders[1].add(e, words, wholes[e].rawMask)
	}
	for g := range index.guards {
		guard := &index.guards[g]
		candidates := wholeHolders[settingsSearchCase(guard.name.caseSensitive)].holding(guard.name.mask, words)
		for e := settingsSearchNextRow(candidates, 0, guard.row); e >= 0; e = settingsSearchNextRow(candidates, e+1, guard.row) {
			if settingsSearchSubsequence(wholes[e].runes(guard.name.caseSensitive), guard.name.pattern) {
				index.relevant[e] = append(index.relevant[e], guard.name)
			}
		}
	}
	return index
}

// guarded reports whether row r is the first row the picker keeps for the
// name over either pre-join text.
func (x *settingsSearchIndex) guarded(r int, name *settingsSearchQuery) bool {
	for t := range x.rows[r].texts {
		if name.matches(&x.rows[r].texts[t]) && x.firstMatch(t, name, r) == r {
			return true
		}
	}
	return false
}

// firstMatch returns the first row up to limit whose pre-join text t matches
// the name, or -1; scans are shared by every row guarding the same name.
func (x *settingsSearchIndex) firstMatch(t int, name *settingsSearchQuery, limit int) int {
	key := settingsSearchFirstKey{text: t, name: name.value}
	scan, ok := x.firsts[key]
	if !ok {
		words := (len(x.rows) + 63) / 64
		scan = &settingsSearchFirst{row: -1, candidates: x.holders[t][settingsSearchCase(name.caseSensitive)].holding(name.mask, words)}
		x.firsts[key] = scan
	}
	end := min(limit+1, len(x.rows))
	for scan.row < 0 && scan.scanned < end {
		next := settingsSearchNextRow(scan.candidates, scan.scanned, end)
		if next < 0 {
			scan.scanned = end
			break
		}
		if name.matches(&x.rows[next].texts[t]) {
			scan.row = next
		}
		scan.scanned = next + 1
	}
	return scan.row
}

// join places row e's label columns around its key and reports whether any
// column was joined.
func (x *settingsSearchIndex) join(e int) (string, bool) {
	row := &x.rows[e]
	relevant := x.relevant[e]

	var before, after string
	joined := false
	// clean records that the current joined text matches no relevant name.
	// Appending after the key then only extends the text, so a new match has
	// to end in the appended piece and must contain its last rune.
	clean := false
	allowed := func(candidate string, appended *settingsSearchText) bool {
		var text *settingsSearchText
		for n := range relevant {
			name := &relevant[n]
			if last := name.pattern[len(name.pattern)-1]; clean && appended != nil &&
				(appended.mask(name.caseSensitive)&settingsSearchRuneBit(last) == 0 || !slices.Contains(appended.runes(name.caseSensitive), last)) {
				continue
			}
			if text == nil {
				built := newSettingsSearchText(candidate)
				text = &built
			}
			if name.matches(text) {
				return false
			}
		}
		return true
	}
	place := func(piece, gap string) bool {
		appended := newSettingsSearchText(piece)
		if next := settingsSearchAppendColumn(after, gap, piece); allowed(settingsSearchJoinedKey(before, row.key, next), &appended) {
			after, joined, clean = next, true, true
			return true
		}
		if next := settingsSearchAppendColumn(before, gap, piece); allowed(settingsSearchJoinedKey(next, row.key, after), nil) {
			before, joined, clean = next, true, true
			return true
		}
		return false
	}
	columns, gaps := settingsSearchLabelColumns(row.label)
	for i, column := range columns {
		// A one-word column would repeat the placement that just failed.
		if place(column, gaps[i]) || !strings.Contains(column, " ") {
			continue
		}
		// A column that cannot be joined whole is joined word by word, so the
		// one word spelling a later option name does not keep the rest of the
		// column (such as its localized words) out of the key.
		for w, word := range strings.Split(column, " ") {
			gap := " "
			if w == 0 {
				gap = gaps[i]
			}
			place(word, gap)
		}
	}
	return settingsSearchJoinedKey(before, row.key, after), joined
}

func settingsSearchAppendColumn(side, gap, column string) string {
	if side == "" {
		return column
	}
	return side + gap + column
}

func settingsSearchJoinedKey(before, key, after string) string {
	if before == "" {
		return key + settingsSearchKeyDelimiter + after
	}
	return before + settingsSearchKeyDelimiter + key + settingsSearchKeyDelimiter + after
}

// settingsSearchLabelColumns splits a stripped label at the runs of two or
// more spaces between its columns, returning each column with the gap that
// preceded it in the label. Tabs become spaces so the key delimiter stays
// unique.
func settingsSearchLabelColumns(label string) (columns, gaps []string) {
	label = strings.ReplaceAll(label, settingsSearchKeyDelimiter, " ")
	gap := ""
	for label != "" {
		end := strings.Index(label, "  ")
		if end < 0 {
			end = len(label)
		}
		columns = append(columns, label[:end])
		gaps = append(gaps, gap)
		rest := label[end:]
		label = strings.TrimLeft(rest, " ")
		gap = rest[:len(rest)-len(label)]
	}
	return columns, gaps
}

// settingsSearchNameColumn is the name column a user reads and types: the
// label without its leading glyph, cut at the first column gap.
func settingsSearchNameColumn(label string) string {
	label = strings.TrimLeftFunc(label, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if idx := strings.Index(label, "  "); idx >= 0 {
		label = label[:idx]
	}
	if idx := strings.Index(label, "\t"); idx >= 0 {
		label = label[:idx]
	}
	return strings.TrimSpace(label)
}

// settingsSearchRowBits marks, for each rune bit, the rows whose text holds a
// rune with that bit.
type settingsSearchRowBits [64][]uint64

func (b *settingsSearchRowBits) add(row, words int, mask uint64) {
	for ; mask != 0; mask &= mask - 1 {
		bit := bits.TrailingZeros64(mask)
		if b[bit] == nil {
			b[bit] = make([]uint64, words)
		}
		b[bit][row/64] |= 1 << uint(row%64)
	}
}

// holding returns the rows whose text holds every bit of mask: the only rows a
// query with that mask can be a subsequence of. It returns nil when no row can.
func (b *settingsSearchRowBits) holding(mask uint64, words int) []uint64 {
	rows := make([]uint64, words)
	for i := range rows {
		rows[i] = ^uint64(0)
	}
	for ; mask != 0; mask &= mask - 1 {
		set := b[bits.TrailingZeros64(mask)]
		if set == nil {
			return nil
		}
		for i := range rows {
			rows[i] &= set[i]
		}
	}
	return rows
}

// settingsSearchNextRow returns the first row in rows at or after from and
// before end, or -1.
func settingsSearchNextRow(rows []uint64, from, end int) int {
	for word := from / 64; word < len(rows) && word*64 < end; word++ {
		set := rows[word]
		if word == from/64 {
			set &= ^uint64(0) << uint(from%64)
		}
		if set != 0 {
			if row := word*64 + bits.TrailingZeros64(set); row < end {
				return row
			}
			return -1
		}
	}
	return -1
}

func settingsSearchCase(caseSensitive bool) int {
	if caseSensitive {
		return 1
	}
	return 0
}

// settingsSearchText is a text the picker matches, with its runes as written
// (case-sensitive queries) and folded the way the picker folds them.
type settingsSearchText struct {
	value       string
	raw, folded []rune
	// rawMask and foldedMask hold settingsSearchRuneBit of every rune.
	rawMask, foldedMask uint64
}

func newSettingsSearchText(value string) settingsSearchText {
	raw := []rune(value)
	folded := make([]rune, len(raw))
	text := settingsSearchText{value: value, raw: raw, folded: folded}
	for i, r := range raw {
		folded[i] = settingsSearchFold(r)
		text.rawMask |= settingsSearchRuneBit(r)
		text.foldedMask |= settingsSearchRuneBit(folded[i])
	}
	return text
}

func (t *settingsSearchText) runes(caseSensitive bool) []rune {
	if caseSensitive {
		return t.raw
	}
	return t.folded
}

func (t *settingsSearchText) mask(caseSensitive bool) uint64 {
	if caseSensitive {
		return t.rawMask
	}
	return t.foldedMask
}

// settingsSearchRuneBit maps a rune to one of 64 bits: ASCII letters and
// digits get their own bit and every other rune shares the last two. A query
// whose bits are not all in a text's mask cannot be a subsequence of it.
func settingsSearchRuneBit(r rune) uint64 {
	switch {
	case r >= 'a' && r <= 'z':
		return 1 << uint(r-'a')
	case r >= 'A' && r <= 'Z':
		return 1 << uint(26+r-'A')
	case r >= '0' && r <= '9':
		return 1 << uint(52+r-'0')
	}
	return 1 << uint(62+r&1)
}

// settingsSearchFold is the picker's case-insensitive rune folding.
func settingsSearchFold(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	if r > unicode.MaxASCII {
		return unicode.ToLower(r)
	}
	return r
}

// settingsSearchQuery is a typed text folded the way the picker folds it: an
// ASCII uppercase letter makes the whole query case-sensitive.
type settingsSearchQuery struct {
	value         string
	pattern       []rune
	mask          uint64
	caseSensitive bool
}

func newSettingsSearchQuery(value string) settingsSearchQuery {
	value = strings.TrimSpace(value)
	caseSensitive := strings.ContainsFunc(value, func(r rune) bool { return r >= 'A' && r <= 'Z' })
	pattern := value
	if !caseSensitive {
		pattern = strings.ToLower(value)
	}
	query := settingsSearchQuery{value: value, pattern: []rune(pattern), caseSensitive: caseSensitive}
	for _, r := range query.pattern {
		query.mask |= settingsSearchRuneBit(r)
	}
	return query
}

// matches reports whether the picker keeps a keyed row with this text for the
// query. A text missing a query rune bit or the query runes in order never
// matches, and a text holding them contiguously always does because every
// picker bonus is non-negative, so only the rest runs the picker's scoring in
// FilterItems.
func (q *settingsSearchQuery) matches(text *settingsSearchText) bool {
	source := text.runes(q.caseSensitive)
	if q.mask&^text.mask(q.caseSensitive) != 0 || !settingsSearchSubsequence(source, q.pattern) {
		return false
	}
	if settingsSearchContains(source, q.pattern) {
		return true
	}
	return len(intpicker.FilterItems([]intpicker.Item{{SearchText: text.value}}, q.value)) == 1
}

func settingsSearchSubsequence(source, pattern []rune) bool {
	matched := 0
	for _, r := range source {
		if matched == len(pattern) {
			break
		}
		if r == pattern[matched] {
			matched++
		}
	}
	return matched == len(pattern)
}

func settingsSearchContains(source, pattern []rune) bool {
	if len(pattern) == 0 {
		return true
	}
	for start := 0; start+len(pattern) <= len(source); start++ {
		if source[start] == pattern[0] && slices.Equal(source[start:start+len(pattern)], pattern) {
			return true
		}
	}
	return false
}

// stripSettingsLabelANSI removes the terminal escape sequences Settings row
// builders style labels with, leaving the text the picker displays.
func stripSettingsLabelANSI(value string) string {
	if !strings.Contains(value, "\x1b") {
		return value
	}
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\x1b' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+1 < len(value) && value[i+1] == '[' {
			i += 2
			for i < len(value) {
				b := value[i]
				i++
				if b >= 0x40 && b <= 0x7e {
					break
				}
			}
			continue
		}
		i += 2
	}
	return out.String()
}

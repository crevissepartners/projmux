package core

import "time"

// MessageArchive holds retained live records followed by history in file order.
// Slices are caller-owned. Records and History preserve the reader's nil/empty
// shape and duplicates, including a message present in both. Skipped counts
// malformed or ref-less history lines, excluding blank lines.
type MessageArchive struct {
	Records []MessageArchiveRecord
	History []MessageHistoryEntry
	Skipped int
}

// MessageArchiveRecord exposes a retained message's full, unredacted Payload.
// Payload access: 권한: 같은 사용자의 StateDir 읽기, 비밀 처리 없음.
// PayloadBytes is the UTF-8 byte length, and Operator is CORE's origin judgment.
// Delivery retains acceptance, terminal state, reason and outcome uncertainty.
type MessageArchiveRecord struct {
	MessageRef, ConversationRef, ReplyTo string
	Origin                               MessageOrigin
	Source, Target                       MessageRoute
	Delivery                             MessageDelivery
	Payload                              string
	PayloadBytes                         int
	Operator                             bool
}

// MessageHistoryEntry is a reclaim-log projection without a body. Known fields
// from higher schemas and both full and narrow routes are preserved. An empty
// PayloadSHA256 means not recorded. Operator is CORE's origin judgment; callers
// need not reproduce that policy. Reason and outcome uncertainty are not logged.
type MessageHistoryEntry struct {
	SchemaVersion                        int
	MessageRef, ConversationRef, ReplyTo string
	State                                MessageState
	AcceptedAt                           time.Time
	PayloadBytes                         int
	PayloadSHA256                        string
	Origin                               MessageOrigin
	Source, Target                       MessageRoute
	Operator                             bool
}

package app

import "io"

// Compatibility sink for an older installed Stop hook. A Stop event never
// publishes or qualifies a coordination reply. The answering Agent must run
// the public agent message send command with an explicit --reply-to.
func runClaudeMessageReply(args []string) error { return runClaudeMessageReplyInput(args, nil, nil) }

func runClaudeMessageReplyInput(_ []string, _ io.Reader, _ func(string) string) error {
	return nil
}

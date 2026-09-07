package app

import "io"

func runClaudeDialogueExec([]string) error                { return errClaudeReplyTool }
func runClaudeDialogueObserver([]string, io.Writer) error { return errClaudeReplyTool }
func cleanupClaudeDialogueProfile(superviseSpec) error    { return nil }

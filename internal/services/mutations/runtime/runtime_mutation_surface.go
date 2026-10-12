package runtime

var ClosedTmuxTopologyMutationVerbs = map[string]bool{
	"new-session": true, "new-window": true, "split-window": true,
	"kill-session": true, "kill-window": true, "kill-pane": true, "kill-server": true,
	"rename-session": true, "rename-window": true,
	"break-pane": true, "join-pane": true, "move-pane": true, "move-window": true,
	"link-window": true, "unlink-window": true, "respawn-pane": true, "respawn-window": true,
	"swap-pane": true, "swap-window": true, "rotate-window": true, "resize-pane": true,
}

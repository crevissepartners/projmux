package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// This module cannot import internal packages. Admission and a missing receipt
// are observable without a provider, tmux server, or CLI subprocess.
func TestExternalPeerMessages(t *testing.T) {
	root := t.TempDir()
	client, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	var receipt core.MessageReceipt
	receipt, err = client.SendMessage(context.Background(), projmux.SendMessageRequest{Target: "uid:agent-missing", Text: "untrusted peer context"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeMessageInvalidInput || receipt.MessageRef != "" {
		t.Fatalf("missing source: %+v %v", receipt, err)
	}
	receipt, err = client.MessageStatus(context.Background(), "message-missing")
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeMessageNotFound || receipt.MessageRef != "" {
		t.Fatalf("missing receipt: %+v %v", receipt, err)
	}
}

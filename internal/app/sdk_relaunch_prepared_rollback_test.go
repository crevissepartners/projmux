package app

import (
	"encoding/json"
	"errors"
	"net"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// A Prepare exchange that fails after the recipe was frozen restores the prior
// recipe once; an acknowledged one keeps it.
func TestFinishSDKPreparedRollsBackOnlyAFailedExchange(t *testing.T) {
	proof := &mutationruntime.ProcessResumePreparation{AgentUID: "agent-a", PaneUID: "pane-a", Conversation: "conversation", LaunchDigest: "digest"}
	t.Run("caller gone", func(t *testing.T) {
		owner, caller := net.Pipe()
		_ = caller.Close()
		session := &processDetachedRelaunchSession{encoder: json.NewEncoder(owner), decoder: json.NewDecoder(owner)}
		rolled, cause := 0, errors.New("rollback unconfirmed")
		result := &agentHostTransferResult{Result: agentRelaunchResult{AgentUID: "agent-a"}, proof: proof, rollback: func() error { rolled++; return cause }}
		err := finishSDKPrepared(session, result)
		if err == nil || rolled != 1 || !errors.Is(err, cause) {
			t.Fatalf("rolled %d err %v", rolled, err)
		}
	})
	t.Run("acknowledged", func(t *testing.T) {
		owner, caller := net.Pipe()
		defer caller.Close()
		go func() {
			var reply processDetachedReply
			decoder := json.NewDecoder(caller)
			if decoder.Decode(&reply) != nil || reply.Phase != "complete" || reply.Preparation == nil || *reply.Preparation != *proof {
				_ = caller.Close()
				return
			}
			_ = json.NewEncoder(caller).Encode("commit")
			var owned string
			_ = decoder.Decode(&owned)
		}()
		session := &processDetachedRelaunchSession{encoder: json.NewEncoder(owner), decoder: json.NewDecoder(owner)}
		rolled := 0
		result := &agentHostTransferResult{Result: agentRelaunchResult{AgentUID: "agent-a"}, proof: proof, rollback: func() error { rolled++; return nil }}
		if err := finishSDKPrepared(session, result); err != nil || rolled != 0 {
			t.Fatalf("rolled %d err %v", rolled, err)
		}
	})
	t.Run("no recipe to restore", func(t *testing.T) {
		owner, caller := net.Pipe()
		_ = caller.Close()
		session := &processDetachedRelaunchSession{encoder: json.NewEncoder(owner), decoder: json.NewDecoder(owner)}
		if err := finishSDKPrepared(session, &agentHostTransferResult{Result: agentRelaunchResult{AgentUID: "agent-a"}}); err == nil {
			t.Fatal("expected the failed exchange")
		}
	})
}

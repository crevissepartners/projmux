package projmux_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func ExampleClient() {
	root, err := os.MkdirTemp("", "projmux-sdk-example-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	client, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "projmux")})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	if _, err := client.WriteInstructions(ctx, "reviewer", []byte("Review carefully.\n")); err != nil {
		panic(err)
	}
	content, err := client.ReadInstructions(ctx, "reviewer")
	if err != nil {
		panic(err)
	}
	fmt.Print(string(content.Content))
	if err := client.DeleteInstructions(ctx, "reviewer"); err != nil {
		panic(err)
	}
	_, err = client.ReadInstructions(ctx, "reviewer")
	var refusal *projmux.Refusal
	if errors.As(err, &refusal) {
		fmt.Println(refusal.Code)
	}
	// Output:
	// Review carefully.
	// persona-not-found
}

func ExampleClient_ListResources() {
	client, err := projmux.NewClient(projmux.Options{StateDir: "/absolute/state/projmux", SocketPath: "/absolute/tmux.sock"})
	if err != nil {
		panic(err)
	}
	list, err := client.ListResources(context.Background(), core.KindAgent, projmux.ResourceSelector{Project: "project/example", Labels: []string{"role=reviewer"}})
	if err != nil {
		var refusal *projmux.Refusal
		if errors.As(err, &refusal) {
			fmt.Println(refusal.Code)
		}
		return
	}
	for _, resource := range list.Items {
		agent := resource.(core.AgentView)
		fmt.Println(agent.Metadata.UID, agent.Status.Phase)
	}
}

func ExampleClient_ObserveRuntime() {
	client, err := projmux.NewClient(projmux.Options{StateDir: "/absolute/state/projmux"})
	if err != nil {
		panic(err)
	}
	report, err := client.ObserveRuntime(context.Background(), projmux.RuntimeRequest{Kind: core.RuntimePanes, SocketPath: "/absolute/tmux.sock"})
	if err != nil {
		panic(err)
	}
	_ = report.Items
	_ = report.Unavailable
}

func ExampleClient_Capabilities() {
	client, _ := projmux.NewClient(projmux.Options{})
	capabilities, _ := client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Provider: "claude"})
	fmt.Println(capabilities.Provider, capabilities.Agent == nil)
	// Output: claude true
}

func ExampleClient_Models() {
	client, _ := projmux.NewClient(projmux.Options{})
	models, _ := client.Models(context.Background(), "claude")
	fmt.Println(models.Provider, models.AcceptsUnlisted)
	// Output: claude true
}

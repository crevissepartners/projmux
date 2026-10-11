package projmux_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/pkg/projmux"
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

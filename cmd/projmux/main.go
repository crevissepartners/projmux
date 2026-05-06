package main

import (
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if app.IsUsageError(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

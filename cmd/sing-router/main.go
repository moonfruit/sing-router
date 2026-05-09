package main

import (
	"os"

	"github.com/moonfruit/sing-router/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

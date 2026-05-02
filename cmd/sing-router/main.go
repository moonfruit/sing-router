package main

import (
	"fmt"
	"os"

	"github.com/moonfruit/sing-router/internal/version"
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "version" || os.Args[1] == "-v" || os.Args[1] == "--version") {
		fmt.Println(version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "sing-router: subcommands not yet wired")
	os.Exit(2)
}

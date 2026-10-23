// Command pilothouse is one inbox for every coding agent: see https://pilothouse.dev.
package main

import (
	"os"

	"github.com/xasdta/pilothouse/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}

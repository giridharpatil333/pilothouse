// Command askgate is one inbox for every coding agent: see https://askgate.dev.
package main

import (
	"os"

	"github.com/xasdta/askgate/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}

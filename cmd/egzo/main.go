// Command egzo orchestrates sandboxed AI coding agents on Docker or Podman.
package main

import (
	"fmt"
	"os"

	"github.com/egzo-ai/egzo/internal/cli"
)

func main() {
	if err := cli.New().Execute(); err != nil {
		if code, ok := cli.IsExitError(err); ok {
			os.Exit(code) // the command ran in a container and said what it had to say
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

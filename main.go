package main

import (
	"os"

	"github.com/tosunlab/timing/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.OSIO()))
}

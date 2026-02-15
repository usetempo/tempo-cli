package main

import (
	"os"

	"github.com/usetempo/tempo-cli/cmd/tempo"
)

var version = "dev"

func main() {
	if err := tempo.Execute(version); err != nil {
		os.Exit(1)
	}
}

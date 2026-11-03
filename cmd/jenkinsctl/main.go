package main

import (
	"os"

	"github.com/priyanshu-shubham/jenkinsctl/internal/cli"
)

// version is set at release time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}

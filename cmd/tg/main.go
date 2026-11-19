// Command tg is a Telegram client for agents and humans.
package main

import (
	"os"

	"github.com/RealDoc06/tg-cli/internal/cli"
)

// version is set by release builds with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() { os.Exit(cli.Main(version)) }

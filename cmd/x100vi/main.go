// Command x100vi reads, writes and previews film-simulation recipes on a Fujifilm X100VI over USB.
package main

import (
	"os"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Main(os.Args[1:], nil, os.Stdout, os.Stderr))
}

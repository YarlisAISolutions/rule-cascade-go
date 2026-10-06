// Command rcas is the Rule Cascade command line: it checks, compiles and evaluates rulesets, serves
// the engine protocol, scaffolds projects, derives rules from API schemas, and serves the Model
// Context Protocol for AI coding agents. Installed as rule-cascade too, it behaves the same and
// calls itself by that name.
//
//	go install rules.sdods.com/go/cmd/rcas@latest
//
// Documentation: https://rules.sdods.com/reference/cli/
package main

import (
	"os"

	"rules.sdods.com/go/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

func main() {
	cli.BuildVersion = version
	os.Exit(cli.Main(cli.ProgramName(os.Args[0]), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

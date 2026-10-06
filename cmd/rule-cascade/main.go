// Command rule-cascade is rcas under its former name, for scripts and pipelines written before the
// rename. It behaves exactly like rcas.
//
//	go install rulescascade.com/go/cmd/rule-cascade@latest
package main

import (
	"os"

	"rulescascade.com/go/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

func main() {
	cli.BuildVersion = version
	os.Exit(cli.Main("rule-cascade", os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

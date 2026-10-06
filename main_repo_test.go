package rulecascade

import (
	"fmt"
	"os"
	"testing"
)

// TestMain runs the tests only inside the Rule Cascade repository: they read the conformance suite,
// the specification and the tools' fixtures from ../../. The public Go mirror (rules.sdods.com/go)
// holds this directory alone, so there `go test ./...` reports that and passes.
func TestMain(m *testing.M) {
	if _, err := os.Stat("../../conformance/README.md"); err != nil {
		fmt.Println("rules.sdods.com/go: the tests need the Rule Cascade repository (../../conformance); skipped")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

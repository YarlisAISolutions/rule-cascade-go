package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain runs the tests only inside the Rule Cascade repository, whose examples and conformance
// suite they read; in the public Go mirror `go test ./...` reports that and passes.
func TestMain(m *testing.M) {
	if _, err := os.Stat(repository + "/conformance/README.md"); err != nil {
		fmt.Println("rcas: the tests need the Rule Cascade repository (conformance/); skipped")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

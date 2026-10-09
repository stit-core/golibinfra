package authnkit_test

import (
	"os"
	"strings"
	"testing"
)

// Блок README § Быстрый старт — программа example_test.go байт в байт: её собирает и запускает
// go test, поэтому сниппет в README правится только вместе с ней.
func TestReadme_QuickStart(t *testing.T) {
	_, rest, _ := strings.Cut(readFile(t, "example_test.go"), "\nimport (")
	imports, rest, _ := strings.Cut(rest, "\n)\n")
	_, rest, _ = strings.Cut(rest, "\nfunc Example() {\n")
	body, _, ok := strings.Cut(rest, "\t// Output:")
	program := "package main\n\nimport (" + imports + "\n)\n\nfunc main() {\n" + body + "}\n"
	if !ok || !strings.Contains(readFile(t, "README.md"), "```go\n"+program+"```\n") {
		t.Fatalf("README.md quick start: the code block is not the program of example_test.go (its imports and the body of Example); edit both:\n%s", program)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// notInCompose are the variables the app reads that compose.yml does not
// pass on, deliberately: each is fixed by the compose file itself, and set
// only to run outside it. See README.md.
var notInCompose = []string{"SIGNAL_API_URL", "LLM_URL"}

// compose.yml lists each variable app gets by name, so a variable added
// here and not there is one that can be set in .env and does nothing. That
// happened to LLM_AGENT_MODEL before it shipped.
func TestComposePassesOnEveryVariable(t *testing.T) {
	t.Parallel()
	read := regexp.MustCompile(`(?:Getenv|envOr|envBool)\("([A-Z0-9_]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var vars []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range read.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(vars, m[1]) {
				vars = append(vars, m[1])
			}
		}
	}
	if len(vars) < 10 {
		t.Fatalf("found only %v; the pattern no longer matches how variables are read", vars)
	}

	compose, err := os.ReadFile(filepath.Join("..", "..", "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		passed := strings.Contains(string(compose), "\n      "+v+": ${"+v)
		switch {
		case slices.Contains(notInCompose, v) && passed:
			t.Errorf("%s is passed on by compose.yml; take it off notInCompose", v)
		case !slices.Contains(notInCompose, v) && !passed:
			t.Errorf("%s is read by the app but not passed on by compose.yml", v)
		}
	}
}

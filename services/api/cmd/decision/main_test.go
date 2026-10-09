package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIListDemoAndInteractive(t *testing.T) {
	for _, args := range [][]string{{"-list"}, {"-scenario", "normal"}, {"-scenario", "conflict", "-once"}, {"-scenario", "unknown", "-json"}, {"-scenario", "empty"}, {"-scenario", "short"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			if err := run(context.Background(), args, strings.NewReader(""), &out); err != nil {
				t.Fatal(err, out.String())
			}
			if out.Len() == 0 {
				t.Fatal("no output")
			}
		})
	}
	var out bytes.Buffer
	commands := "adopt 1\nreject 2 user-b\nbatch user-b\nretry\nsession\nbudget 200\nquery 周末做什么\nhistory\nquit\n"
	if err := run(context.Background(), []string{"-interactive"}, strings.NewReader(commands), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "操作失败") || !strings.Contains(out.String(), "新 Session") {
		t.Fatal(out.String())
	}
}

func TestCLIErrorsPreserveCurrentResult(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-scenario", "missing"}, strings.NewReader(""), &out); err == nil {
		t.Fatal("unknown scenario ignored")
	}
	out.Reset()
	commands := "adopt 999\nadopt 1 outsider\nbudget -1\nretry\nsession\nquit\n"
	if err := run(context.Background(), []string{"-interactive"}, strings.NewReader(commands), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "操作失败，原结果保留") != 4 || !strings.Contains(out.String(), "latestDecisionId") {
		t.Fatal(out.String())
	}
}

func TestFixtureExportAndReload(t *testing.T) {
	var exported bytes.Buffer
	if err := run(context.Background(), []string{"-dump-fixture"}, strings.NewReader(""), &exported); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, exported.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-fixture", path, "-scenario", "personal", "-once"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "recommended") {
		t.Fatal(output.String())
	}
}

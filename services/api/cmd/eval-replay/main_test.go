package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReportNeverReplacesExistingPrivateReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested/report.json")
	if err := writeReport(path, map[string]string{"secret": "private"}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeReport(path, map[string]string{"changed": "no"}); err == nil {
		t.Fatal("overwrote report")
	}
	after, _ := os.ReadFile(path)
	if string(first) != string(after) {
		t.Fatal("existing report changed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("report is not private")
	}
}
func TestDryRunIgnoresProductionAndModelConfiguration(t *testing.T) {
	t.Setenv("JETAIME_DATABASE_URL", "postgres://not-allowed.invalid/prod")
	t.Setenv("JETAIME_PHOENIX_TRACES_ENDPOINT", "http://not-allowed.invalid/v1/traces")
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	path := filepath.Join(t.TempDir(), "report.json")
	os.Args = []string{"eval-replay", "-snapshot", "../../../../evals/replay/messages.example.jsonl", "-cases", "../../../../evals/replay/cases.example.json", "-dry-run", "-trace", "-model-config", "does-not-exist.json", "-output", path}
	if err := execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result report
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Model != nil || result.ValidatedCases != 2 || len(result.Results) != 0 {
		t.Fatal("dry run executed inference")
	}
}

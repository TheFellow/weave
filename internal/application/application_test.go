package application

import (
	"testing"

	"github.com/TheFellow/weave/internal/architecture"
	"github.com/TheFellow/weave/internal/storage"
)

func TestCIFatalIssueClassification(t *testing.T) {
	warnings := []storage.Issue{{Severity: storage.IssueWarning, Kind: "unresolved-occurrence", Record: "external"}}
	if hasFatalIssues(warnings) {
		t.Fatal("open-world unresolved occurrence was fatal")
	}
	errors := append(warnings, storage.Issue{Severity: storage.IssueError, Kind: "orphan-document", Record: "broken"})
	if !hasFatalIssues(errors) {
		t.Fatal("ownership damage was not fatal")
	}
}

func TestIntegrityIssuesAreVisibleInSARIF(t *testing.T) {
	log := architecture.SARIF(architecture.Config{Schema: architecture.Schema}, architecture.Report{Schema: architecture.ReportSchema})
	attachIntegritySARIF(&log, []storage.Issue{
		{Severity: storage.IssueWarning, Kind: "unresolved-occurrence", Record: "external", Detail: "not materialized"},
		{Severity: storage.IssueError, Kind: "orphan-document", Record: "broken", Detail: "unit absent"},
	})
	if len(log.Runs) != 1 || len(log.Runs[0].Results) != 2 {
		t.Fatalf("SARIF = %#v", log)
	}
	if log.Runs[0].Results[0].Level != "warning" || log.Runs[0].Results[1].Level != "error" {
		t.Fatalf("results = %#v", log.Runs[0].Results)
	}
}

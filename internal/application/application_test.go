package application

import (
	"testing"

	"github.com/TheFellow/weave/internal/architecture"
	"github.com/TheFellow/weave/internal/graph"
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
	config := architecture.Config{Schema: architecture.Schema, Rules: []architecture.Rule{{ID: "boundary", Action: "forbid"}}}
	report := architecture.Report{Schema: architecture.ReportSchema, Violations: []architecture.Violation{{
		RuleID: "boundary", Message: "boundary crossed", Document: "main.go",
		Range: graph.Range{Start: graph.Position{Line: 2, Column: 3}, End: graph.Position{Line: 2, Column: 4}},
	}}}
	log := architecture.SARIF(config, report)
	attachIntegritySARIF(&log, []storage.Issue{
		{Severity: storage.IssueWarning, Kind: "unresolved-occurrence", Record: "external", Detail: "not materialized"},
		{Severity: storage.IssueError, Kind: "orphan-document", Record: "broken", Detail: "unit absent"},
	})
	if len(log.Runs) != 1 || len(log.Runs[0].Results) != 1 || len(log.Runs[0].Invocations) != 1 {
		t.Fatalf("SARIF = %#v", log)
	}
	for _, result := range log.Runs[0].Results {
		if len(result.Locations) == 0 {
			t.Fatalf("source result has no location: %#v", result)
		}
	}
	notifications := log.Runs[0].Invocations[0].ToolExecutionNotifications
	if len(notifications) != 2 || notifications[0].Level != "warning" || notifications[1].Level != "error" || log.Runs[0].Invocations[0].ExecutionSuccessful {
		t.Fatalf("notifications = %#v", notifications)
	}
}

package adapter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBlackBoxConformanceAgainstPythonAdapter(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python is unavailable")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "protocol", "adapter", "v0", "conformance", "fixture_adapter.py"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join("..", "..", "protocol", "adapter", "v0", "conformance", "repository"))
	if err != nil {
		t.Fatal(err)
	}
	report := Conform(context.Background(), Executable{Path: python, Args: []string{script}}, fixture, Permissions{})
	if !report.Passed {
		t.Fatalf("report = %#v", report)
	}
}

func TestDescribeEnforcesHostOutputBoundAgainstPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is unavailable")
	}
	script, _ := filepath.Abs(filepath.Join("..", "..", "protocol", "adapter", "v0", "conformance", "fixture_adapter.py"))
	_, _, err = (Runner{Limits: Limits{MaxFrameBytes: 1, MaxStderrBytes: 1}}).Describe(context.Background(), Executable{Path: python, Args: []string{script}})
	if err == nil {
		t.Fatal("one-byte describe limit succeeded")
	}
}

func TestBlackBoxConformanceAgainstGoAdapter(t *testing.T) {
	t.Setenv("WEAVE_GO_CONFORMANCE_HELPER", "1")
	fixture, _ := filepath.Abs(filepath.Join("..", "..", "protocol", "adapter", "v0", "conformance", "repository"))
	report := Conform(context.Background(), Executable{Path: os.Args[0], Args: []string{"-test.run=TestGoConformanceHelper", "--"}}, fixture, Permissions{})
	if !report.Passed {
		t.Fatalf("report = %#v", report)
	}
}

func TestBlackBoxConformanceRejectsAdaptersThatAcceptInvalidInput(t *testing.T) {
	fixture, _ := filepath.Abs(filepath.Join("..", "..", "protocol", "adapter", "v0", "conformance", "repository"))
	for _, test := range []struct {
		mode, check string
	}{
		{mode: "wrong-protocol", check: "wrong-protocol-rejected"},
		{mode: "malformed-request", check: "malformed-request-rejected"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("WEAVE_GO_CONFORMANCE_HELPER", "1")
			t.Setenv("WEAVE_GO_CONFORMANCE_ACCEPT_INVALID", test.mode)
			report := Conform(context.Background(), Executable{Path: os.Args[0], Args: []string{"-test.run=TestGoConformanceHelper", "--"}}, fixture, Permissions{})
			if report.Passed {
				t.Fatalf("accepted invalid %s: %#v", test.mode, report)
			}
			for _, check := range report.Checks {
				if check.Name == test.check {
					if check.Passed {
						t.Fatalf("check %q passed: %#v", test.check, report)
					}
					return
				}
			}
			t.Fatalf("check %q is absent: %#v", test.check, report)
		})
	}
}

func TestGoConformanceHelper(t *testing.T) {
	if os.Getenv("WEAVE_GO_CONFORMANCE_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	operation := os.Args[separator+1]
	if operation == "describe" && separator+3 < len(os.Args) && os.Args[separator+3] == Protocol {
		_ = json.NewEncoder(os.Stdout).Encode(fixtureCapabilities())
		os.Exit(0)
	}
	if operation == "describe" && os.Getenv("WEAVE_GO_CONFORMANCE_ACCEPT_INVALID") == "wrong-protocol" {
		os.Exit(0)
	}
	if operation != "index" || separator+3 >= len(os.Args) || os.Args[separator+3] != Protocol {
		os.Exit(2)
	}
	var request IndexRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil || request.RequestID == "" || request.RepositoryRoot == "" {
		if os.Getenv("WEAVE_GO_CONFORMANCE_ACCEPT_INVALID") == "malformed-request" {
			os.Exit(0)
		}
		os.Exit(2)
	}
	writeValidRun(request.RequestID, false)
	os.Exit(0)
}

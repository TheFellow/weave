package command_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
)

func TestPlaceholderCommandsSucceedSilently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "init", args: []string{"init"}, want: "init"},
		{name: "index", args: []string{"index"}, want: "index"},
		{name: "status", args: []string{"status"}, want: "status"},
		{name: "symbols", args: []string{"symbols"}, want: "symbols"},
		{name: "definition", args: []string{"definition"}, want: "definition"},
		{name: "references", args: []string{"references"}, want: "references"},
		{name: "callers", args: []string{"callers"}, want: "callers"},
		{name: "callees", args: []string{"callees"}, want: "callees"},
		{name: "path", args: []string{"path"}, want: "path"},
		{name: "impact", args: []string{"impact"}, want: "impact"},
		{name: "dependencies", args: []string{"dependencies"}, want: "dependencies"},
		{name: "architecture check", args: []string{"architecture", "check"}, want: "architecture check"},
		{name: "repos add", args: []string{"repos", "add"}, want: "repos add"},
		{name: "repos remove", args: []string{"repos", "remove"}, want: "repos remove"},
		{name: "repos list", args: []string{"repos", "list"}, want: "repos list"},
		{name: "adapters list", args: []string{"adapters", "list"}, want: "adapters list"},
		{name: "adapters doctor", args: []string{"adapters", "doctor"}, want: "adapters doctor"},
		{name: "export", args: []string{"export"}, want: "export"},
		{name: "verify", args: []string{"verify"}, want: "verify"},
		{name: "gc", args: []string{"gc"}, want: "gc"},
		{name: "version", args: []string{"version"}, want: "version"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app := &recordingApplication{}
			root := command.New(app, command.Streams{
				Stdin:  strings.NewReader(""),
				Stdout: &stdout,
				Stderr: &stderr,
			})

			args := append([]string{"weave"}, test.args...)
			if err := root.Run(context.Background(), args); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := stdout.String(); got != "" {
				t.Errorf("stdout = %q, want empty", got)
			}
			if got := stderr.String(); got != "" {
				t.Errorf("stderr = %q, want empty", got)
			}

			want := []application.Invocation{{Command: test.want}}
			if !reflect.DeepEqual(app.invocations, want) {
				t.Errorf("invocations = %#v, want %#v", app.invocations, want)
			}
		})
	}
}

func TestApplicationErrorIsReturned(t *testing.T) {
	t.Parallel()

	want := errors.New("application failed")
	app := &recordingApplication{err: want}
	root := command.New(app, command.Streams{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})

	err := root.Run(context.Background(), []string{"weave", "index"})
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want %v", err, want)
	}
}

func TestInvalidInvocationsReturnErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"unknown"}},
		{name: "unknown nested command", args: []string{"repos", "unknown"}},
		{name: "unknown flag", args: []string{"status", "--unknown"}},
		{name: "unexpected argument", args: []string{"status", "unexpected"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app := &recordingApplication{}
			root := command.New(app, command.Streams{
				Stdin:  strings.NewReader(""),
				Stdout: &stdout,
				Stderr: &stderr,
			})

			args := append([]string{"weave"}, test.args...)
			if err := root.Run(context.Background(), args); err == nil {
				t.Fatal("Run() error = nil, want usage error")
			}
			if len(app.invocations) != 0 {
				t.Errorf("application invoked on invalid input: %#v", app.invocations)
			}
		})
	}
}

type recordingApplication struct {
	invocations []application.Invocation
	err         error
}

func (a *recordingApplication) Execute(_ context.Context, invocation application.Invocation) error {
	a.invocations = append(a.invocations, invocation)
	return a.err
}

// Package command constructs Weave's command-line interface.
package command

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/TheFellow/weave/internal/application"
	cli "github.com/urfave/cli/v3"
)

// Streams are the process streams used by the command tree. Keeping them
// explicit makes output ownership testable and prevents command actions from
// reaching for process globals.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// New returns the complete Weave command tree.
func New(app application.Service, streams Streams) *cli.Command {
	root := &cli.Command{
		Name:      "weave",
		Usage:     "build and query a local semantic index",
		Reader:    streams.Stdin,
		Writer:    streams.Stdout,
		ErrWriter: streams.Stderr,
		// The executable boundary owns presentation and process exit. Command.Run
		// remains a normal, directly testable Go function.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}

	root.Commands = []*cli.Command{
		leaf(app, "init", "initialize Weave for a repository"),
		leaf(app, "index", "build or refresh the semantic index"),
		leaf(app, "status", "show index and freshness status"),
		leaf(app, "symbols", "find symbols"),
		leaf(app, "definition", "find symbol definitions"),
		leaf(app, "references", "find symbol references"),
		leaf(app, "callers", "find callers of a symbol"),
		leaf(app, "callees", "find symbols called by a symbol"),
		leaf(app, "path", "find a bounded path between symbols"),
		leaf(app, "impact", "find code affected by a change"),
		leaf(app, "dependencies", "find semantic dependencies"),
		group("architecture", "evaluate architecture rules",
			leaf(app, "architecture check", "check architecture rules"),
		),
		group("repos", "manage the cross-repository catalog",
			leaf(app, "repos add", "add a repository to the catalog"),
			leaf(app, "repos remove", "remove a repository from the catalog"),
			leaf(app, "repos list", "list cataloged repositories"),
		),
		group("adapters", "inspect semantic adapters",
			leaf(app, "adapters list", "list available adapters"),
			leaf(app, "adapters doctor", "diagnose adapter availability"),
		),
		leaf(app, "export", "export normalized semantic facts"),
		leaf(app, "verify", "verify index integrity and reproducibility"),
		leaf(app, "gc", "compact and garbage-collect derived data"),
		leaf(app, "version", "show the Weave version"),
	}

	return root
}

func group(name, usage string, children ...*cli.Command) *cli.Command {
	return &cli.Command{
		Name:     name,
		Usage:    usage,
		Commands: children,
	}
}

func leaf(app application.Service, path, usage string) *cli.Command {
	name := path
	if index := strings.LastIndexByte(path, ' '); index >= 0 {
		name = path[index+1:]
	}

	return &cli.Command{
		Name:  name,
		Usage: usage,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 0 {
				return cli.Exit(fmt.Sprintf("%s accepts no arguments", path), 2)
			}
			return app.Execute(ctx, application.Invocation{
				Command:   path,
				Arguments: append([]string(nil), cmd.Args().Slice()...),
			})
		},
	}
}

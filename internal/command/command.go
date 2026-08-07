// Package command constructs Weave's command-line interface.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/graph"
	cli "github.com/urfave/cli/v3"
)

type Streams struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// New returns the complete Weave command tree.
func New(app application.Service, streams Streams) *cli.Command {
	root := &cli.Command{
		Name: "weave", Usage: "build and query a local semantic index",
		Reader: streams.Stdin, Writer: streams.Stdout, ErrWriter: streams.Stderr,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	root.Commands = []*cli.Command{
		lifecycle(app, streams, "init", "initialize Weave for a repository"),
		indexCommand(app, streams),
		lifecycle(app, streams, "status", "show index and freshness status"),
		lookup(app, streams, "symbols", "find symbols"),
		lookup(app, streams, "definition", "find symbol definitions"),
		lookup(app, streams, "references", "find symbol references"),
		lookup(app, streams, "callers", "find callers of a symbol"),
		lookup(app, streams, "callees", "find symbols called by a symbol"),
		traversal(app, streams, "path", "find a bounded path between symbols", 2),
		traversal(app, streams, "impact", "find code affected by a symbol", 1),
		noop(app, streams, "dependencies", "find semantic dependencies"),
		architectureCommand(app, streams),
		repositoryCommands(app, streams),
		group("adapters", "inspect semantic adapters", adapterInspection(app, streams, "list", "list available adapters"), adapterInspection(app, streams, "doctor", "diagnose adapter availability")),
		maintenance(app, streams, "export", "export normalized semantic facts", true),
		maintenance(app, streams, "verify", "verify index integrity", true),
		maintenance(app, streams, "gc", "compact derived data", false),
		noop(app, streams, "version", "show the Weave version"),
	}
	return root
}

func architectureCommand(app application.Service, streams Streams) *cli.Command {
	check := &cli.Command{
		Name: "check", Usage: "check repository architecture rules",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Usage: "repository-relative or absolute rule configuration"},
			&cli.StringFlag{Name: "format", Value: "text", Usage: "output format: text, json, or sarif", Validator: func(value string) error {
				if value != "text" && value != "json" && value != "sarif" {
					return fmt.Errorf("must be text, json, or sarif")
				}
				return nil
			}},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 0 {
				return cli.Exit("architecture check expects no arguments", 2)
			}
			format := cmd.String("format")
			invocationFormat := format
			if invocationFormat == "text" {
				invocationFormat = ""
			}
			response, err := app.Execute(ctx, application.Invocation{Command: "architecture check", JSON: format == "json", Format: invocationFormat, ConfigPath: cmd.String("config")})
			if err != nil {
				return err
			}
			if err := renderInvocation(streams, response, format == "json"); err != nil {
				return err
			}
			if response.Failed {
				return cli.Exit("architecture rules failed", 3)
			}
			return nil
		},
	}
	return &cli.Command{Name: "architecture", Aliases: []string{"arch"}, Usage: "evaluate architecture rules", Commands: []*cli.Command{check}}
}

func repositoryCommands(app application.Service, streams Streams) *cli.Command {
	flags := func(jsonOutput bool) []cli.Flag {
		result := []cli.Flag{&cli.StringFlag{Name: "catalog", Usage: "absolute catalog database path"}}
		if jsonOutput {
			result = append(result, jsonFlag())
		}
		return result
	}
	return group("repos", "manage the explicit cross-repository catalog",
		&cli.Command{Name: "add", Usage: "register one repository worktree", Flags: flags(true), Action: invokeCatalog(app, streams, "repos add", 0, 1)},
		&cli.Command{Name: "remove", Usage: "remove a worktree by key, identity, or absolute root", Flags: flags(false), Action: invokeCatalog(app, streams, "repos remove", 1, 1)},
		&cli.Command{Name: "list", Usage: "list registered repository worktrees", Flags: flags(true), Action: invokeCatalog(app, streams, "repos list", 0, 0)},
		&cli.Command{Name: "status", Usage: "diagnose registered repository worktrees", Flags: flags(true), Action: invokeCatalog(app, streams, "repos status", 0, 0)},
		&cli.Command{Name: "sync", Usage: "refresh registered repository metadata", Flags: flags(true), Action: invokeCatalog(app, streams, "repos sync", 0, 100)},
	)
}

func invokeCatalog(app application.Service, streams Streams, path string, minimum, maximum int) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		if count := cmd.Args().Len(); count < minimum || count > maximum {
			return cli.Exit(fmt.Sprintf("%s expects %s", path, arity(minimum, maximum)), 2)
		}
		response, err := app.Execute(ctx, application.Invocation{
			Command: path, Arguments: append([]string(nil), cmd.Args().Slice()...), JSON: cmd.Bool("json"),
			CatalogPath: cmd.String("catalog"),
		})
		if err != nil {
			return err
		}
		return renderInvocation(streams, response, cmd.Bool("json"))
	}
}

func adapterInspection(app application.Service, streams Streams, name, usage string) *cli.Command {
	return &cli.Command{Name: name, Usage: usage, Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, "adapters "+name, 0, 0)}
}

func group(name, usage string, children ...*cli.Command) *cli.Command {
	return &cli.Command{Name: name, Usage: usage, Commands: children}
}

func lookup(app application.Service, streams Streams, name, usage string) *cli.Command {
	return &cli.Command{Name: name, Usage: usage, Flags: append([]cli.Flag{jsonFlag(), limitFlag()}, federationFlags()...), Action: invoke(app, streams, name, 1, 1)}
}

func traversal(app application.Service, streams Streams, name, usage string, arguments int) *cli.Command {
	return &cli.Command{Name: name, Usage: usage, Flags: []cli.Flag{
		jsonFlag(), limitFlag(), &cli.IntFlag{Name: "max-depth", Value: 8, Usage: "maximum traversal depth", Validator: func(v int) error {
			if v < 1 || v > 100 {
				return fmt.Errorf("must be between 1 and 100")
			}
			return nil
		}},
		&cli.StringSliceFlag{Name: "kind", Usage: "edge kind to traverse (repeatable)"},
		&cli.StringFlag{Name: "scope", Value: "local", Usage: "query scope: local or catalog", Validator: validateScope},
		&cli.StringSliceFlag{Name: "repo", Usage: "catalog repository identity, key, or root (repeatable)"},
		&cli.StringFlag{Name: "catalog", Usage: "absolute catalog database path"},
		&cli.IntFlag{Name: "max-repos", Value: 32, Usage: "maximum catalog fan-out", Validator: validateMaxRepos},
	}, Action: invoke(app, streams, name, arguments, arguments)}
}

func federationFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "scope", Value: "local", Usage: "query scope: local or catalog", Validator: validateScope},
		&cli.StringSliceFlag{Name: "repo", Usage: "catalog repository identity, key, or root (repeatable)"},
		&cli.StringFlag{Name: "catalog", Usage: "absolute catalog database path"},
		&cli.IntFlag{Name: "max-repos", Value: 32, Usage: "maximum catalog fan-out", Validator: validateMaxRepos},
	}
}

func validateScope(value string) error {
	if value != "local" && value != "catalog" {
		return fmt.Errorf("must be local or catalog")
	}
	return nil
}

func validateMaxRepos(value int) error {
	if value < 1 || value > 256 {
		return fmt.Errorf("must be between 1 and 256")
	}
	return nil
}

func maintenance(app application.Service, streams Streams, name, usage string, jsonOutput bool) *cli.Command {
	var flags []cli.Flag
	if jsonOutput {
		flags = append(flags, jsonFlag())
	}
	return &cli.Command{Name: name, Usage: usage, Flags: flags, Action: invoke(app, streams, name, 0, 0)}
}

func lifecycle(app application.Service, streams Streams, name, usage string) *cli.Command {
	return &cli.Command{Name: name, Usage: usage, Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, name, 0, 0)}
}

func indexCommand(app application.Service, streams Streams) *cli.Command {
	return &cli.Command{
		Name: "index", Usage: "build or refresh the semantic index",
		Flags: []cli.Flag{
			jsonFlag(),
			&cli.StringFlag{Name: "scip", Usage: "explicitly import a SCIP protobuf index"},
			&cli.StringFlag{Name: "adapter", Usage: "explicitly run a native adapter executable"},
			&cli.StringSliceFlag{Name: "adapter-arg", Usage: "literal adapter argument before its operation (repeatable)"},
			&cli.DurationFlag{Name: "timeout", Value: 2 * time.Minute, Usage: "external adapter deadline", Validator: func(value time.Duration) error {
				if value <= 0 || value > time.Hour {
					return fmt.Errorf("must be greater than zero and at most one hour")
				}
				return nil
			}},
			&cli.BoolFlag{Name: "allow-network", Usage: "permit the adapter to use the network"},
			&cli.BoolFlag{Name: "allow-restore", Usage: "permit dependency restore"},
			&cli.BoolFlag{Name: "allow-build-tool", Usage: "permit build-tool invocation"},
			&cli.BoolFlag{Name: "allow-generators", Usage: "permit generator execution"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 0 {
				return cli.Exit("index expects no arguments", 2)
			}
			scipPath, adapterPath := cmd.String("scip"), cmd.String("adapter")
			if scipPath != "" && adapterPath != "" {
				return cli.Exit("--scip and --adapter are mutually exclusive", 2)
			}
			adapterArgs := cmd.StringSlice("adapter-arg")
			if len(adapterArgs) == 0 {
				adapterArgs = nil
			}
			permissions := adapter.Permissions{
				Network: cmd.Bool("allow-network"), Restore: cmd.Bool("allow-restore"),
				BuildTool: cmd.Bool("allow-build-tool"), RunGenerators: cmd.Bool("allow-generators"),
			}
			if adapterPath == "" && (len(adapterArgs) != 0 || permissions != (adapter.Permissions{})) {
				return cli.Exit("adapter arguments and permissions require --adapter", 2)
			}
			if scipPath == "" && adapterPath == "" {
				response, err := app.Execute(ctx, application.Invocation{Command: "index", JSON: cmd.Bool("json")})
				if err != nil {
					return err
				}
				return renderInvocation(streams, response, cmd.Bool("json"))
			}
			timeout := time.Duration(0)
			if adapterPath != "" {
				timeout = cmd.Duration("timeout")
			}
			response, err := app.Execute(ctx, application.Invocation{
				Command: "index", JSON: cmd.Bool("json"), SCIPPath: scipPath, AdapterPath: adapterPath,
				AdapterArgs: adapterArgs, Timeout: timeout, Permissions: permissions,
			})
			if err != nil {
				return err
			}
			return renderInvocation(streams, response, cmd.Bool("json"))
		},
	}
}

func noop(app application.Service, streams Streams, path, usage string) *cli.Command {
	name := path
	if index := strings.LastIndexByte(path, ' '); index >= 0 {
		name = path[index+1:]
	}
	return &cli.Command{Name: name, Usage: usage, Action: invoke(app, streams, path, 0, 0)}
}

func jsonFlag() cli.Flag { return &cli.BoolFlag{Name: "json", Usage: "emit versioned JSON"} }
func limitFlag() cli.Flag {
	return &cli.IntFlag{Name: "limit", Value: 50, Usage: "maximum results", Validator: func(v int) error {
		if v < 1 || v > 100000 {
			return fmt.Errorf("must be between 1 and 100000")
		}
		return nil
	}}
}

func invoke(app application.Service, streams Streams, path string, minArgs, maxArgs int) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		if count := cmd.Args().Len(); count < minArgs || count > maxArgs {
			return cli.Exit(fmt.Sprintf("%s expects %s", path, arity(minArgs, maxArgs)), 2)
		}
		var kinds []graph.EdgeKind
		for _, value := range cmd.StringSlice("kind") {
			kind := graph.EdgeKind(value)
			if !graph.IsEdgeKind(kind) {
				return cli.Exit(fmt.Sprintf("unknown edge kind %q", value), 2)
			}
			kinds = append(kinds, kind)
		}
		response, err := app.Execute(ctx, application.Invocation{
			Command: path, Arguments: append([]string(nil), cmd.Args().Slice()...), JSON: cmd.Bool("json"),
			Limit: cmd.Int("limit"), MaxDepth: cmd.Int("max-depth"), Kinds: kinds,
			Scope: queryScope(cmd), Repositories: cmd.StringSlice("repo"), CatalogPath: cmd.String("catalog"), MaxRepos: cmd.Int("max-repos"),
		})
		if err != nil {
			return err
		}
		return renderInvocation(streams, response, cmd.Bool("json"))
	}
}

func queryScope(cmd *cli.Command) string {
	if len(cmd.StringSlice("repo")) > 0 {
		return "catalog"
	}
	return cmd.String("scope")
}

func renderInvocation(streams Streams, response application.Response, jsonOutput bool) error {
	if response.Freshness != nil && response.Freshness.Refreshed {
		if _, err := fmt.Fprintf(streams.Stderr, "index: refreshed %d changed paths\n", response.Freshness.ChangeCount); err != nil {
			return err
		}
	}
	for _, diagnostic := range response.Diagnostics {
		if _, err := fmt.Fprintln(streams.Stderr, diagnostic); err != nil {
			return err
		}
	}
	return render(streams.Stdout, response, jsonOutput)
}

func arity(minimum, maximum int) string {
	if minimum == maximum {
		if minimum == 0 {
			return "no arguments"
		}
		if minimum == 1 {
			return "one argument"
		}
		return fmt.Sprintf("%d arguments", minimum)
	}
	return fmt.Sprintf("between %d and %d arguments", minimum, maximum)
}

func render(writer io.Writer, response application.Response, jsonOutput bool) error {
	if response.SARIF != nil {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response.SARIF)
	}
	if jsonOutput {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		if response.Architecture != nil {
			return encoder.Encode(response.Architecture)
		}
		if response.Export != nil {
			return encoder.Encode(struct {
				Schema string `json:"schema"`
				Facts  any    `json:"facts"`
			}{"weave.export/v1", response.Export})
		}
		return encoder.Encode(response)
	}
	if response.Command == "status" && response.Freshness != nil {
		status := response.Freshness
		_, err := fmt.Fprintf(writer, "current\t%t\ndirty\t%t\nchanges\t%d\nrepository\t%s\nworktree\t%s\n", status.Current, status.Dirty, status.ChangeCount, status.RepositoryIdentity, status.WorktreeID)
		if err != nil {
			return err
		}
		if status.Reason != "" {
			_, err = fmt.Fprintf(writer, "reason\t%s\n", status.Reason)
		}
		return err
	}
	for _, symbol := range response.Symbols {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s:%d:%d\n", symbol.ID, symbol.Kind, symbol.DisplayName, symbol.DocumentID, symbol.Definition.Start.Line+1, symbol.Definition.Start.Column+1); err != nil {
			return err
		}
	}
	for _, occurrence := range response.Occurrences {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s:%d:%d\n", occurrence.SymbolID, occurrence.Role, occurrence.DocumentID, occurrence.Range.Start.Line+1, occurrence.Range.Start.Column+1); err != nil {
			return err
		}
	}
	for _, edge := range response.Edges {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", edge.From, edge.Kind, edge.To); err != nil {
			return err
		}
	}
	if len(response.Edges) == 0 {
		for _, node := range response.Nodes {
			if _, err := fmt.Fprintln(writer, node); err != nil {
				return err
			}
		}
	}
	for _, issue := range response.Issues {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", issue.Kind, issue.Record, issue.Detail); err != nil {
			return err
		}
	}
	for _, adapter := range response.Adapters {
		status := "missing"
		if adapter.Available {
			status = "available"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", adapter.Name, adapter.Kind, status, adapter.Path, adapter.Detail); err != nil {
			return err
		}
	}
	for _, repository := range response.Repositories {
		state := "current"
		if repository.Missing {
			state = "missing"
		} else if repository.Stale {
			state = "stale"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", repository.Identity, repository.WorktreeID, state, repository.Root, repository.DatabasePath); err != nil {
			return err
		}
	}
	if response.Architecture != nil {
		for _, violation := range response.Architecture.Violations {
			location := violation.Document
			if location != "" {
				location = fmt.Sprintf("%s:%d:%d", location, violation.Range.Start.Line+1, violation.Range.Start.Column+1)
			}
			if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", violation.RuleID, violation.Kind, violation.From, violation.To, location, violation.Message); err != nil {
				return err
			}
		}
	}
	if response.Export != nil {
		for _, unit := range response.Export.Units {
			if _, err := fmt.Fprintf(writer, "unit\t%s\n", unit.ID); err != nil {
				return err
			}
		}
		for _, document := range response.Export.Documents {
			if _, err := fmt.Fprintf(writer, "document\t%s\t%s\n", document.ID, document.Path); err != nil {
				return err
			}
		}
		for _, symbol := range response.Export.Symbols {
			if _, err := fmt.Fprintf(writer, "symbol\t%s\t%s\n", symbol.ID, symbol.DisplayName); err != nil {
				return err
			}
		}
		for _, occurrence := range response.Export.Occurrences {
			if _, err := fmt.Fprintf(writer, "occurrence\t%s\t%s\n", occurrence.ID, occurrence.SymbolID); err != nil {
				return err
			}
		}
		for _, edge := range response.Export.Edges {
			if _, err := fmt.Fprintf(writer, "edge\t%s\t%s\t%s\t%s\n", edge.ID, edge.From, edge.Kind, edge.To); err != nil {
				return err
			}
		}
	}
	if response.Truncated {
		_, err := fmt.Fprintln(writer, "... truncated")
		return err
	}
	return nil
}

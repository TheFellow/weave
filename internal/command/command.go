// Package command constructs Weave's command-line interface.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/dot"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
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
		impactCommand(app, streams),
		lookup(app, streams, "dependencies", "find direct semantic dependencies"),
		graphCommand(app, streams),
		linkCommands(app, streams),
		workspaceCommands(app, streams),
		architectureCommand(app, streams),
		repositoryCommands(app, streams),
		ciCommands(app, streams),
		group("adapters", "inspect semantic adapters", adapterInspection(app, streams, "list", "list available adapters"), adapterInspection(app, streams, "doctor", "diagnose adapter availability")),
		maintenance(app, streams, "export", "export normalized semantic facts", true),
		maintenance(app, streams, "verify", "verify index integrity", true),
		maintenance(app, streams, "gc", "compact derived data", false),
		versionCommand(app, streams),
	}
	return root
}

func linkCommands(app application.Service, streams Streams) *cli.Command {
	write := func(name, usage string, adding bool) *cli.Command {
		flags := []cli.Flag{
			jsonFlag(),
			&cli.StringFlag{Name: "from", Usage: "source query, exact entity:<id>, or intentional open id:<id>"},
			&cli.StringFlag{Name: "to", Usage: "target query, exact entity:<id>, or intentional open id:<id>"},
			&cli.StringFlag{Name: "kind", Usage: "normalized relationship kind"},
			&cli.StringFlag{Name: "note", Usage: "reviewable human context retained in the declaration"},
		}
		flags = append(flags, federationFlags()...)
		return &cli.Command{Name: name, Usage: usage, UsageText: "weave links " + name + " LINK_ID [options]", Flags: flags, Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return cli.Exit("links "+name+" expects one link ID", 2)
			}
			fromSet, toSet := cmd.IsSet("from"), cmd.IsSet("to")
			kindSet, noteSet := cmd.IsSet("kind"), cmd.IsSet("note")
			if adding && (!fromSet || !toSet || !kindSet) {
				return cli.Exit("links add requires --from, --to, and --kind", 2)
			}
			if !adding && !fromSet && !toSet && !kindSet && !noteSet {
				return cli.Exit("links update requires at least one of --from, --to, --kind, or --note", 2)
			}
			kind := graph.EdgeKind(cmd.String("kind"))
			if kindSet && !graph.IsEdgeKind(kind) {
				return cli.Exit(fmt.Sprintf("unknown edge kind %q", kind), 2)
			}
			response, err := app.Execute(ctx, application.Invocation{
				Command: "links " + name, Arguments: append([]string(nil), cmd.Args().Slice()...), JSON: cmd.Bool("json"),
				LinkFrom: cmd.String("from"), LinkTo: cmd.String("to"), LinkKind: kind, LinkNote: cmd.String("note"),
				LinkFromSet: fromSet, LinkToSet: toSet, LinkKindSet: kindSet, LinkNoteSet: noteSet,
				Scope: queryScope(cmd), Repositories: cmd.StringSlice("repo"), CatalogPath: cmd.String("catalog"), MaxRepos: cmd.Int("max-repos"),
			})
			if err != nil {
				return err
			}
			return renderInvocation(streams, response, cmd.Bool("json"))
		}}
	}
	list := &cli.Command{Name: "list", Usage: "list authored contextual relationships", UsageText: "weave links list [--json]", Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, "links list", 0, 0)}
	remove := &cli.Command{Name: "remove", Aliases: []string{"rm"}, Usage: "remove an authored contextual relationship", UsageText: "weave links remove LINK_ID [--json]", Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, "links remove", 1, 1)}
	return &cli.Command{Name: "links", Aliases: []string{"link"}, Usage: "author contextual relationships between indexed resources", Commands: []*cli.Command{
		write("add", "add an exact contextual relationship", true),
		write("update", "update an authored contextual relationship", false),
		remove,
		list,
	}}
}

func graphCommand(app application.Service, streams Streams) *cli.Command {
	flags := []cli.Flag{
		jsonFlag(),
		&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "write DOT to this file instead of stdout"},
		&cli.StringFlag{Name: "direction", Value: string(query.DirectionBoth), Usage: "traversal direction: incoming, outgoing, or both", Validator: func(value string) error {
			switch query.Direction(value) {
			case query.DirectionIncoming, query.DirectionOutgoing, query.DirectionBoth:
				return nil
			default:
				return fmt.Errorf("must be incoming, outgoing, or both")
			}
		}},
		&cli.StringSliceFlag{Name: "kind", Usage: "edge kind to include (repeatable; default high-level relationships)"},
		&cli.IntFlag{Name: "max-depth", Value: 3, Usage: "maximum traversal depth", Validator: func(value int) error {
			if value < 1 || value > 100 {
				return fmt.Errorf("must be between 1 and 100")
			}
			return nil
		}},
		&cli.IntFlag{Name: "limit", Value: 100, Usage: "maximum graph nodes", Validator: func(value int) error {
			if value < 1 || value > 5000 {
				return fmt.Errorf("must be between 1 and 5000")
			}
			return nil
		}},
		&cli.IntFlag{Name: "max-edges", Value: 400, Usage: "maximum graph edges examined", Validator: func(value int) error {
			if value < 1 || value > 20000 {
				return fmt.Errorf("must be between 1 and 20000")
			}
			return nil
		}},
	}
	flags = append(flags, federationFlags()...)
	return &cli.Command{
		Name: "graph", Usage: "render a bounded semantic neighborhood as Graphviz DOT",
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return cli.Exit("graph expects one argument", 2)
			}
			output := cmd.String("output")
			if cmd.Bool("json") && output != "" && output != "-" {
				return cli.Exit("--output writes DOT and cannot be combined with --json", 2)
			}
			kinds, err := parseEdgeKinds(cmd.StringSlice("kind"))
			if err != nil {
				return cli.Exit(err.Error(), 2)
			}
			response, err := app.Execute(ctx, application.Invocation{
				Command: "graph", Arguments: append([]string(nil), cmd.Args().Slice()...), JSON: cmd.Bool("json"),
				Limit: cmd.Int("limit"), MaxDepth: cmd.Int("max-depth"), MaxEdges: cmd.Int("max-edges"), Kinds: kinds,
				Direction: query.Direction(cmd.String("direction")), Scope: queryScope(cmd), Repositories: cmd.StringSlice("repo"),
				CatalogPath: cmd.String("catalog"), MaxRepos: cmd.Int("max-repos"),
			})
			if err != nil {
				return err
			}
			if output == "" || output == "-" || cmd.Bool("json") {
				return renderInvocation(streams, response, cmd.Bool("json"))
			}
			var content bytes.Buffer
			fileStreams := streams
			fileStreams.Stdout = &content
			if err := renderInvocation(fileStreams, response, false); err != nil {
				return err
			}
			if err := os.WriteFile(output, content.Bytes(), 0o644); err != nil {
				return fmt.Errorf("write DOT output: %w", err)
			}
			return nil
		},
	}
}

func workspaceCommands(app application.Service, streams Streams) *cli.Command {
	child := func(name, usage string) *cli.Command {
		flags := []cli.Flag{
			jsonFlag(), limitFlag(),
			&cli.IntFlag{Name: "max-depth", Value: 8, Usage: "maximum containment depth", Validator: func(value int) error {
				if value < 1 || value > 100 {
					return fmt.Errorf("must be between 1 and 100")
				}
				return nil
			}},
		}
		flags = append(flags, federationFlags()...)
		return &cli.Command{Name: name, Usage: usage, Flags: flags, Action: invoke(app, streams, "workspace "+name, 1, 1)}
	}
	return &cli.Command{Name: "workspace", Aliases: []string{"ws"}, Usage: "navigate files and structured content as a semantic graph", Commands: []*cli.Command{
		child("find", "find files, documents, sections, routes, topics, and resources"),
		child("outline", "show a document or directory containment tree"),
		child("links", "show links and semantic content relationships"),
		child("backlinks", "show incoming content relationships"),
	}}
}

func ciCommands(app application.Service, streams Streams) *cli.Command {
	key := &cli.Command{Name: "key", Usage: "print the deterministic disposable-index cache key", Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, "ci key", 0, 0)}
	index := &cli.Command{Name: "index", Usage: "force a deterministic CI index refresh", Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, "ci index", 0, 0)}
	check := &cli.Command{
		Name: "check", Usage: "verify the index and check architecture policy",
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
				return cli.Exit("ci check expects no arguments", 2)
			}
			format := cmd.String("format")
			invocationFormat := format
			if format == "text" {
				invocationFormat = ""
			}
			response, err := app.Execute(ctx, application.Invocation{Command: "ci check", JSON: format == "json", Format: invocationFormat, ConfigPath: cmd.String("config")})
			if err != nil {
				return err
			}
			if err := renderInvocation(streams, response, format == "json"); err != nil {
				return err
			}
			if response.Failed {
				return cli.Exit("CI checks failed", 3)
			}
			return nil
		},
	}
	return group("ci", "run deterministic CI workflows", key, index, check)
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

func versionCommand(app application.Service, streams Streams) *cli.Command {
	return &cli.Command{Name: "version", Usage: "show the Weave version", Flags: []cli.Flag{jsonFlag()}, Action: invoke(app, streams, "version", 0, 0)}
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

func impactCommand(app application.Service, streams Streams) *cli.Command {
	return &cli.Command{Name: "impact", Usage: "find code and tests affected by symbols, files, packages, or a Git diff", Flags: []cli.Flag{
		jsonFlag(), limitFlag(), &cli.IntFlag{Name: "max-depth", Value: 8, Usage: "maximum traversal depth", Validator: func(v int) error {
			if v < 1 || v > 100 {
				return fmt.Errorf("must be between 1 and 100")
			}
			return nil
		}},
		&cli.StringSliceFlag{Name: "kind", Usage: "edge kind to traverse (repeatable)"},
		&cli.StringSliceFlag{Name: "file", Usage: "repository-relative changed file root (repeatable)"},
		&cli.StringSliceFlag{Name: "package", Usage: "semantic package root (repeatable)"},
		&cli.StringFlag{Name: "git-diff", Usage: "Git revision to compare with the current working tree"},
		&cli.StringFlag{Name: "scope", Value: "local", Usage: "query scope: local or catalog", Validator: validateScope},
		&cli.StringSliceFlag{Name: "repo", Usage: "catalog repository identity, key, or root (repeatable)"},
		&cli.StringFlag{Name: "catalog", Usage: "absolute catalog database path"},
		&cli.IntFlag{Name: "max-repos", Value: 32, Usage: "maximum catalog fan-out", Validator: validateMaxRepos},
	}, Action: func(ctx context.Context, cmd *cli.Command) error {
		files, packages, revision := cmd.StringSlice("file"), cmd.StringSlice("package"), cmd.String("git-diff")
		rooted := len(files) != 0 || len(packages) != 0 || revision != ""
		if rooted && cmd.Args().Len() != 0 {
			return cli.Exit("impact accepts either one symbol or file/package/Git roots, not both", 2)
		}
		if !rooted && cmd.Args().Len() != 1 {
			return cli.Exit("impact expects one symbol or at least one --file, --package, or --git-diff root", 2)
		}
		if rooted && queryScope(cmd) == "catalog" {
			return cli.Exit("file, package, and Git-diff impact roots require --scope local", 2)
		}
		kinds, err := parseEdgeKinds(cmd.StringSlice("kind"))
		if err != nil {
			return cli.Exit(err.Error(), 2)
		}
		response, err := app.Execute(ctx, application.Invocation{
			Command: "impact", Arguments: append([]string(nil), cmd.Args().Slice()...), JSON: cmd.Bool("json"),
			Limit: cmd.Int("limit"), MaxDepth: cmd.Int("max-depth"), Kinds: kinds,
			Scope: queryScope(cmd), Repositories: cmd.StringSlice("repo"), CatalogPath: cmd.String("catalog"), MaxRepos: cmd.Int("max-repos"),
			ImpactFiles: files, ImpactPackages: packages, DiffRevision: revision,
		})
		if err != nil {
			return err
		}
		return renderInvocation(streams, response, cmd.Bool("json"))
	}}
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
		kinds, parseErr := parseEdgeKinds(cmd.StringSlice("kind"))
		if parseErr != nil {
			return cli.Exit(parseErr.Error(), 2)
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

func parseEdgeKinds(values []string) ([]graph.EdgeKind, error) {
	var kinds []graph.EdgeKind
	for _, value := range values {
		kind := graph.EdgeKind(value)
		if !graph.IsEdgeKind(kind) {
			return nil, fmt.Errorf("unknown edge kind %q", value)
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func queryScope(cmd *cli.Command) string {
	if len(cmd.StringSlice("repo")) > 0 {
		return "catalog"
	}
	return cmd.String("scope")
}

func renderInvocation(streams Streams, response application.Response, jsonOutput bool) error {
	if response.Freshness != nil && response.Freshness.Refreshed && response.Command != "ci index" {
		if _, err := fmt.Fprintf(streams.Stderr, "index: refreshed %d changed paths\n", response.Freshness.ChangeCount); err != nil {
			return err
		}
	}
	if response.Freshness != nil {
		for _, diagnostic := range response.Freshness.Diagnostics {
			if _, err := fmt.Fprintln(streams.Stderr, diagnostic); err != nil {
				return err
			}
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
		if response.Architecture != nil && response.Command == "architecture check" {
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
	if response.Command == "graph" {
		focus := ""
		if len(response.Nodes) != 0 {
			focus = response.Nodes[0]
		}
		return dot.Write(writer, dot.View{
			Focus: focus, Nodes: response.Nodes, Symbols: response.Symbols,
			Edges: response.Edges, Truncated: response.Truncated,
		})
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
	if response.Command == "ci key" && response.CI != nil {
		_, err := fmt.Fprintln(writer, response.CI.CacheKey)
		return err
	}
	if response.Command == "version" && response.Version != nil {
		_, err := fmt.Fprintf(writer, "weave %s (%s/%s, %s)", response.Version.Version, response.Version.OS, response.Version.Arch, response.Version.GoVersion)
		if err != nil {
			return err
		}
		if response.Version.Commit != "" {
			if _, err := fmt.Fprintf(writer, " commit %s", response.Version.Commit); err != nil {
				return err
			}
		}
		if response.Version.Dirty {
			if _, err := fmt.Fprint(writer, " dirty"); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintln(writer)
		return err
	}
	if strings.HasPrefix(response.Command, "workspace ") {
		return renderWorkspace(writer, response)
	}
	if strings.HasPrefix(response.Command, "links ") {
		for _, link := range response.Links {
			if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", link.ID, link.Kind, link.From, link.To, strconv.Quote(link.Note)); err != nil {
				return err
			}
		}
		return nil
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
	for _, test := range response.Tests {
		if _, err := fmt.Fprintf(writer, "test\t%s\t%s\t%s:%d:%d\n", test.ID, test.DisplayName, test.DocumentID, test.Definition.Start.Line+1, test.Definition.Start.Column+1); err != nil {
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
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", issue.Severity, issue.Kind, issue.Record, issue.Detail); err != nil {
			return err
		}
	}
	for _, adapter := range response.Adapters {
		status := "missing"
		if adapter.Available {
			status = "available"
		}
		if adapter.Checked && !adapter.Compatible {
			status = "incompatible"
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

func renderWorkspace(writer io.Writer, response application.Response) error {
	byID := make(map[string]graph.Symbol, len(response.Symbols))
	for _, symbol := range response.Symbols {
		byID[symbol.ID] = symbol
	}
	if response.Command == "workspace links" || response.Command == "workspace backlinks" {
		for _, edge := range response.Edges {
			endpoint := edge.To
			if response.Command == "workspace backlinks" {
				endpoint = edge.From
			}
			label := "[unmaterialized endpoint]"
			if symbol, ok := byID[endpoint]; ok {
				label = symbol.StableName
			}
			if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", edge.Kind, label, edge.Evidence); err != nil {
				return err
			}
		}
		return renderTruncation(writer, response.Truncated)
	}
	for _, symbol := range response.Symbols {
		location := symbol.StableName
		if symbol.DocumentID != "" {
			location = fmt.Sprintf("%s:%d:%d", symbol.StableName, symbol.Definition.Start.Line+1, symbol.Definition.Start.Column+1)
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", symbol.Kind, location, symbol.DisplayName, symbol.Evidence); err != nil {
			return err
		}
	}
	return renderTruncation(writer, response.Truncated)
}

func renderTruncation(writer io.Writer, truncated bool) error {
	if !truncated {
		return nil
	}
	_, err := fmt.Fprintln(writer, "... truncated")
	return err
}

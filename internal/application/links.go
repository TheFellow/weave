package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/federation"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/storage"
)

func (app Local) links(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	path := bridge.Path(repo.Root)
	lockPath := filepath.Join(repo.StorageDir, "links.lock")

	switch invocation.Command {
	case "links list":
		config, err := bridge.Load(path)
		if err != nil {
			return Response{}, err
		}
		response.Links = canonicalLinks(config.Links)
		return response, nil
	case "links remove":
		var removed bridge.Link
		if err := bridge.Edit(ctx, path, lockPath, func(config *bridge.Config) error {
			index := linkIndex(config.Links, invocation.Arguments[0])
			if index < 0 {
				return fmt.Errorf("link %q does not exist", invocation.Arguments[0])
			}
			removed = config.Links[index]
			config.Links = append(config.Links[:index:index], config.Links[index+1:]...)
			return nil
		}); err != nil {
			return Response{}, err
		}
		response.Links = []bridge.Link{removed}
		return app.refreshAuthoredLinks(ctx, response)
	case "links add", "links update":
		return app.writeLink(ctx, response, invocation, path, lockPath)
	default:
		return Response{}, fmt.Errorf("unsupported links command %q", invocation.Command)
	}
}

func (app Local) writeLink(ctx context.Context, response Response, invocation Invocation, path, lockPath string) (Response, error) {
	id := invocation.Arguments[0]
	if invocation.Command == "links add" && (!invocation.LinkFromSet || !invocation.LinkToSet || !invocation.LinkKindSet) {
		return Response{}, errors.New("links add requires from, to, and kind")
	}
	if invocation.Command == "links update" && !invocation.LinkFromSet && !invocation.LinkToSet && !invocation.LinkKindSet && !invocation.LinkNoteSet {
		return Response{}, errors.New("links update requires at least one changed field")
	}
	if invocation.LinkKindSet && !validLinkKind(invocation.LinkKind) {
		return Response{}, fmt.Errorf("unknown relationship kind %q", invocation.LinkKind)
	}
	queries := make([]string, 0, 2)
	if invocation.LinkFromSet {
		queries = append(queries, invocation.LinkFrom)
	}
	if invocation.LinkToSet {
		queries = append(queries, invocation.LinkTo)
	}
	resolved, diagnostics, sources, err := app.resolveLinkEndpoints(ctx, invocation, queries)
	if err != nil {
		return Response{}, err
	}
	response.Diagnostics = append(response.Diagnostics, diagnostics...)
	response.Sources = append(response.Sources, sources...)
	var from, to string
	next := 0
	if invocation.LinkFromSet {
		from = bridge.Entity(resolved[next])
		next++
	}
	if invocation.LinkToSet {
		to = bridge.Entity(resolved[next])
	}

	var link bridge.Link
	if err := bridge.Edit(ctx, path, lockPath, func(config *bridge.Config) error {
		index := linkIndex(config.Links, id)
		if invocation.Command == "links add" && index >= 0 {
			return fmt.Errorf("link %q already exists", id)
		}
		if invocation.Command == "links update" && index < 0 {
			return fmt.Errorf("link %q does not exist", id)
		}
		link = bridge.Link{ID: id}
		if index >= 0 {
			link = config.Links[index]
		}
		if invocation.LinkFromSet {
			link.From = from
		}
		if invocation.LinkToSet {
			link.To = to
		}
		if invocation.LinkKindSet {
			link.Kind = invocation.LinkKind
		}
		if invocation.LinkNoteSet {
			link.Note = invocation.LinkNote
		}
		if index < 0 {
			config.Links = append(config.Links, link)
		} else {
			config.Links[index] = link
		}
		return nil
	}); err != nil {
		return Response{}, err
	}
	response.Links = []bridge.Link{link}
	return app.refreshAuthoredLinks(ctx, response)
}

func (app Local) resolveLinkEndpoints(ctx context.Context, invocation Invocation, values []string) ([]string, []string, []federation.Source, error) {
	result := make([]string, len(values))
	var unresolved []int
	for i, value := range values {
		if id, ok, err := explicitEndpoint(value); err != nil {
			return nil, nil, nil, err
		} else if ok {
			result[i] = id
		} else {
			unresolved = append(unresolved, i)
		}
	}
	if len(unresolved) == 0 {
		return result, nil, nil, nil
	}

	if invocation.Scope == "catalog" {
		path, err := catalog.DefaultPath(firstNonempty(invocation.CatalogPath, app.CatalogPath))
		if err != nil {
			return nil, nil, nil, err
		}
		maxRepos := invocation.MaxRepos
		if maxRepos == 0 {
			maxRepos = 32
		}
		var freshnessDiagnostics []string
		store, err := federation.OpenFresh(ctx, path, invocation.Repositories, maxRepos, func(ctx context.Context, root string) error {
			if app.FreshnessFor == nil {
				return errors.New("automatic freshness is unavailable in this application")
			}
			manager := app.FreshnessFor(root)
			if manager == nil {
				return errors.New("freshness manager is unavailable")
			}
			status, err := manager.Ensure(ctx, false)
			for _, diagnostic := range status.Diagnostics {
				freshnessDiagnostics = append(freshnessDiagnostics, status.RepositoryIdentity+": "+diagnostic)
			}
			return err
		})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, index := range unresolved {
			symbol, resolveErr := query.ResolveUnique(ctx, store, values[index])
			if resolveErr != nil {
				_ = store.Close()
				return nil, nil, nil, fmt.Errorf("resolve link endpoint %q: %w", values[index], resolveErr)
			}
			result[index] = symbol.ID
		}
		diagnostics := append(freshnessDiagnostics, store.Diagnostics()...)
		sources := store.Sources()
		closeErr := store.Close()
		if closeErr != nil {
			return nil, nil, nil, closeErr
		}
		slices.Sort(diagnostics)
		return result, slices.Compact(diagnostics), sources, nil
	}

	if app.Freshness != nil {
		if _, err := app.Freshness.Ensure(ctx, false); err != nil {
			return nil, nil, nil, err
		}
	}
	databasePath, err := app.databasePath(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := storage.Open(ctx, databasePath, storage.Options{MustExist: true})
	if err != nil {
		return nil, nil, nil, err
	}
	defer db.Close()
	for _, index := range unresolved {
		symbol, resolveErr := query.ResolveUnique(ctx, db, values[index])
		if resolveErr != nil {
			return nil, nil, nil, fmt.Errorf("resolve link endpoint %q: %w", values[index], resolveErr)
		}
		result[index] = symbol.ID
	}
	return result, nil, nil, nil
}

func explicitEndpoint(value string) (string, bool, error) {
	if strings.HasPrefix(value, "id:") {
		id := strings.TrimPrefix(value, "id:")
		if id == "" || strings.IndexByte(id, 0) >= 0 {
			return "", false, fmt.Errorf("invalid open endpoint %q", value)
		}
		return id, true, nil
	}
	if strings.HasPrefix(value, "entity:") || strings.HasPrefix(value, "symbol:") {
		id, err := bridge.Endpoint(value)
		return id, err == nil, err
	}
	return "", false, nil
}

func (app Local) refreshAuthoredLinks(ctx context.Context, response Response) (Response, error) {
	if app.Freshness == nil {
		return response, nil
	}
	status, err := app.Freshness.Ensure(ctx, false)
	response.Freshness = &status
	if err != nil {
		return Response{}, fmt.Errorf("refresh authored links: %w", err)
	}
	return response, nil
}

func linkIndex(links []bridge.Link, id string) int {
	return slices.IndexFunc(links, func(link bridge.Link) bool { return link.ID == id })
}

func canonicalLinks(links []bridge.Link) []bridge.Link {
	result := append([]bridge.Link(nil), links...)
	slices.SortFunc(result, func(a, b bridge.Link) int { return strings.Compare(a.ID, b.ID) })
	return result
}

// Keep the edge vocabulary in this application boundary explicit for tools
// constructing invocations without the CLI.
func validLinkKind(kind graph.EdgeKind) bool { return graph.IsEdgeKind(kind) }

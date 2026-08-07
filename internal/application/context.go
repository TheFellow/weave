package application

import (
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/federation"
	"github.com/TheFellow/weave/internal/repository"
)

func contextOptions(invocation Invocation) contextquery.Options {
	scope := invocation.Scope
	if scope == "" {
		scope = "local"
	}
	return contextquery.Options{
		Scope: scope, Limit: invocation.Limit, ContextLines: invocation.ContextLines,
		MaxSourceBytes: invocation.MaxSourceBytes,
	}
}

func localLocator(repo repository.Repository) contextquery.Locator {
	value := contextquery.Repository{Identity: repo.Identity, WorktreeID: repo.WorktreeID, Root: repo.Root}
	return func(_, _ string) []contextquery.Repository { return []contextquery.Repository{value} }
}

func federatedLocator(store *federation.Store) contextquery.Locator {
	return func(kind, id string) []contextquery.Repository {
		sources := store.SourcesFor(kind, id)
		result := make([]contextquery.Repository, 0, len(sources))
		for _, source := range sources {
			result = append(result, contextquery.Repository{
				Identity: source.Repository, WorktreeID: source.WorktreeID, Root: source.Root,
			})
		}
		return result
	}
}

func contextTruncated(value contextquery.Truncation) bool {
	return value.Occurrences || value.Incoming || value.Outgoing || value.Source
}

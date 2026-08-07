// Package application defines the boundary between Weave's delivery surfaces
// and its use cases.
package application

import "context"

// Invocation identifies a validated CLI operation. Arguments are copied at the
// presentation boundary so an implementation may safely retain the invocation.
// Concrete request and result types will replace generic arguments as commands
// acquire behavior.
type Invocation struct {
	Command   string
	Arguments []string
}

// Service executes Weave use cases. The CLI depends on this interface rather
// than storage, Git, or language adapters directly.
type Service interface {
	Execute(context.Context, Invocation) error
}

// Noop is the explicit milestone-one application. It implements no indexing or
// query behavior and deliberately produces no output.
type Noop struct{}

// Execute satisfies Service without pretending that a use case is implemented.
func (Noop) Execute(context.Context, Invocation) error { return nil }

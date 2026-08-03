package orchestrator

import "context"

type Orchestrator interface {
	Replace(context.Context, Topology) error
	Snapshot(context.Context) (Topology, string, error)
	Delete(context.Context) error
	CreateGroup(context.Context, Group) error
	ReplaceGroup(context.Context, string, Group) error
	DeleteGroup(context.Context, string) error
}

var _ Orchestrator = (*Repository)(nil)

package registry

import "context"

type Priority int

const (
	PriorityCritical Priority = iota
	PriorityBusiness
	PriorityEdge
)

func (p Priority) String() string {
	switch p {
	case PriorityCritical:
		return "critical"
	case PriorityBusiness:
		return "business"
	case PriorityEdge:
		return "edge"
	default:
		return "unknown"
	}
}

type Status int

const (
	StatusUninitialized Status = iota
	StatusStarting
	StatusReady
	StatusStopped
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusUninitialized:
		return "uninitialized"
	case StatusStarting:
		return "starting"
	case StatusReady:
		return "ready"
	case StatusStopped:
		return "stopped"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type Service interface {
	Name() string
	Priority() Priority
	Status() Status
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

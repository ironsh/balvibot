package approval

import (
	"context"
	"encoding/json"
	"fmt"
)

// ErrUnknownAction is returned by Dispatch when no handler is registered for
// the requested action name.
var ErrUnknownAction = fmt.Errorf("unknown action")

// Handler executes a single approved action. args is the raw JSON the agent
// enqueued. A non-nil error marks the action failed.
type Handler func(ctx context.Context, args json.RawMessage) error

// Registry maps action names to their handlers.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Register binds a handler to an action name, overwriting any prior handler.
func (r *Registry) Register(action string, h Handler) {
	r.handlers[action] = h
}

// Dispatch runs the handler for action. It returns ErrUnknownAction if none is
// registered, or the handler's error.
func (r *Registry) Dispatch(ctx context.Context, action string, args json.RawMessage) error {
	h, ok := r.handlers[action]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownAction, action)
	}
	return h(ctx, args)
}

// Has reports whether an action has a registered handler.
func (r *Registry) Has(action string) bool {
	_, ok := r.handlers[action]
	return ok
}

package strego

import "context"

// Handler processes tasks of a specific type.
type Handler interface {
	ProcessTask(ctx context.Context, task *Task) error
}

// HandlerFunc is an adapter to allow the use of ordinary functions as handlers.
type HandlerFunc func(ctx context.Context, task *Task) error

// ProcessTask calls f(ctx, task).
func (f HandlerFunc) ProcessTask(ctx context.Context, task *Task) error {
	return f(ctx, task)
}

// ServeMux is a task handler multiplexer.
// It matches the type of each incoming task against a list of registered patterns
// and calls the handler that matches.
type ServeMux struct {
	handlers map[string]Handler
}

// NewServeMux creates a new ServeMux.
func NewServeMux() *ServeMux {
	return &ServeMux{
		handlers: make(map[string]Handler),
	}
}

// Handle registers the handler for the given task type.
func (m *ServeMux) Handle(taskType string, handler Handler) {
	if handler == nil {
		panic("strego: nil handler")
	}
	if taskType == "" {
		panic("strego: empty task type")
	}
	if _, exists := m.handlers[taskType]; exists {
		panic("strego: multiple registrations for " + taskType)
	}
	m.handlers[taskType] = handler
}

// HandleFunc registers the handler function for the given task type.
func (m *ServeMux) HandleFunc(taskType string, handler func(ctx context.Context, task *Task) error) {
	m.Handle(taskType, HandlerFunc(handler))
}

// Handler returns the handler for the given task type.
// Returns nil if no handler is registered.
func (m *ServeMux) Handler(taskType string) Handler {
	return m.handlers[taskType]
}

// Types returns all registered task types.
func (m *ServeMux) Types() []string {
	types := make([]string, 0, len(m.handlers))
	for t := range m.handlers {
		types = append(types, t)
	}
	return types
}

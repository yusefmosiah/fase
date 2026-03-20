package adapterapi

import "context"

// Input is a single piece of user input for a live session turn.
type Input struct {
	Text string
}

// TextInput creates a text Input.
func TextInput(text string) Input {
	return Input{Text: text}
}

// EventKind classifies a live agent event.
type EventKind string

const (
	EventKindSessionStarted  EventKind = "session.started"
	EventKindSessionResumed  EventKind = "session.resumed"
	EventKindSessionClosed   EventKind = "session.closed"
	EventKindTurnStarted     EventKind = "turn.started"
	EventKindTurnCompleted   EventKind = "turn.completed"
	EventKindTurnFailed      EventKind = "turn.failed"
	EventKindTurnInterrupted EventKind = "turn.interrupted"
	EventKindOutputDelta     EventKind = "output.delta"
	EventKindError           EventKind = "error"
)

// Event is a structured notification emitted by a live session.
type Event struct {
	Kind      EventKind
	SessionID string
	TurnID    string
	// Text carries delta output, error message, or a human-readable summary.
	Text string
}

// SteerEvent is a pre-formatted message to relay to the running agent mid-turn.
type SteerEvent struct {
	Message string
}

// StartSessionRequest configures a new live session.
type StartSessionRequest struct {
	CWD     string
	Model   string
	Profile string
	// SteerCh delivers system events to the agent mid-turn via turn/steer.
	// If nil, no automatic steering from external events is performed.
	SteerCh <-chan SteerEvent
}

// LiveAgentAdapter creates and resumes live sessions.
type LiveAgentAdapter interface {
	Name() string
	StartSession(ctx context.Context, req StartSessionRequest) (LiveSession, error)
	ResumeSession(ctx context.Context, nativeSessionID string, req StartSessionRequest) (LiveSession, error)
}

// LiveSession is a persistent conversational execution context.
type LiveSession interface {
	// SessionID returns the adapter-native session identifier.
	SessionID() string

	// ActiveTurnID returns the current active turn ID, or empty string if no turn is active.
	ActiveTurnID() string

	// StartTurn begins a new turn in the session and returns the native turn ID.
	StartTurn(ctx context.Context, input []Input) (string, error)

	// Steer injects additional input into the currently active turn.
	// expectedTurnID must match the active turn; returns an error on mismatch.
	Steer(ctx context.Context, expectedTurnID string, input []Input) error

	// Interrupt cancels the currently active turn.
	Interrupt(ctx context.Context) error

	// Events returns a channel of structured events from this session.
	// The channel is closed when the session is closed.
	Events() <-chan Event

	// Close shuts down the session and its underlying transport.
	Close() error
}

package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// opEvent is one event from a streamed backup operation: either a log/progress
// line, or the terminal done marker carrying the operation's error (nil on
// success) and an optional typed payload (e.g. an UploadResult).
type opEvent struct {
	line    string
	done    bool
	err     error
	payload any
}

// opStream bridges a blocking backup operation (running in a goroutine) to the
// Bubbletea update loop. The operation writes lines through sink(); the UI
// consumes them one at a time via wait(). The buffered channel provides
// backpressure so fast rclone progress output cannot outrun the UI.
type opStream struct {
	ch chan opEvent
}

func newOpStream() *opStream { return &opStream{ch: make(chan opEvent, 64)} }

// sink returns a backup.Logger sink that forwards each line as an event.
func (s *opStream) sink() func(string) {
	return func(line string) { s.ch <- opEvent{line: line} }
}

// finish reports the operation's result and closes the stream.
func (s *opStream) finish(err error) { s.finishWith(nil, err) }

// finishWith reports the result with a typed payload, then closes the stream.
func (s *opStream) finishWith(payload any, err error) {
	s.ch <- opEvent{done: true, err: err, payload: payload}
	close(s.ch)
}

// wait returns a command that blocks for the next event. Returning nil on a
// closed channel ends the read loop.
func (s *opStream) wait() tea.Cmd {
	ch := s.ch
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

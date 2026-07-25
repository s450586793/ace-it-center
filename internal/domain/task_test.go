package domain

import "testing"

func TestCanTransitionAllowsTaskLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from TaskState
		to   TaskState
		want bool
	}{
		{name: "queue to dispatch", from: TaskQueued, to: TaskDispatched, want: true},
		{name: "dispatch to running", from: TaskDispatched, to: TaskRunning, want: true},
		{name: "running to success", from: TaskRunning, to: TaskSucceeded, want: true},
		{name: "running to failure", from: TaskRunning, to: TaskFailed, want: true},
		{name: "terminal state cannot restart", from: TaskSucceeded, to: TaskRunning, want: false},
		{name: "queue cannot skip to success", from: TaskQueued, to: TaskSucceeded, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}


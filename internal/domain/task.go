package domain

type TaskState string

const (
	TaskQueued     TaskState = "queued"
	TaskDispatched TaskState = "dispatched"
	TaskRunning    TaskState = "running"
	TaskSucceeded  TaskState = "succeeded"
	TaskFailed     TaskState = "failed"
	TaskCancelled  TaskState = "cancelled"
	TaskTimedOut   TaskState = "timed_out"
)

func CanTransition(from, to TaskState) bool {
	allowed := map[TaskState]map[TaskState]bool{
		TaskQueued: {
			TaskDispatched: true,
			TaskCancelled:  true,
			TaskTimedOut:   true,
		},
		TaskDispatched: {
			TaskRunning:   true,
			TaskFailed:    true,
			TaskCancelled: true,
			TaskTimedOut:  true,
		},
		TaskRunning: {
			TaskSucceeded: true,
			TaskFailed:    true,
			TaskCancelled: true,
			TaskTimedOut:  true,
		},
	}
	return allowed[from][to]
}


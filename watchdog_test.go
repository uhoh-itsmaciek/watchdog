package watchdog

import (
	"testing"
	"time"
)

type taskInfo struct {
	Schedule time.Duration
	Timeout time.Duration
	Executions []execInfo
}

type execInfo struct {
	Error error
	Duration time.Duration
}


// check
//  + simple execution
//  + multiple tasks
//  - stalls
//    - no duplicate stalls ?
//  - recovery
//  - stalls with multiple tasks
//  - recovery with multiple tasks
//  - multiple stalls with multiple tasks
//  - multiple recoveries with multiple tasks

var workloads = []struct{
	WatchDuration time.Duration
	Tasks []taskInfo
}{
	{
		Tasks: []taskInfo{
			{
				Schedule: 10 * time.Millisecond,
				Timeout: 1 * time.Hour,
				Executions: []execInfo{
					{Error: nil, Duration: time.Millisecond},
					{Error: nil, Duration: time.Millisecond},
				},
			},
		},
	},
}

func drainExecutions(execs map[*Task][]*Execution, execCh <- chan *Execution, done chan <- bool) {
	for e := range execCh {
		execs[e.Task] = append(execs[e.Task], e)
	}
	done <- true
}

func drainStalls(stalls map[*Task][]*Stall, stallCh <- chan *Stall, done chan <- bool) {
	for s := range stallCh {
		stalls[s.Task] = append(stalls[s.Task], s)
	}
	done <- true
}

// true if b happened within d of a
func within(a, b time.Time, delta time.Duration) bool {
	return a.Before(b) && b.Sub(a) < delta
}

func TestScheduling(t *testing.T) {
	for i, workload := range workloads {
		execCounts := make(map[*Task]int)
		tasks := make([]*Task, len(workload.Tasks))
		taskMap := make(map[*Task]*taskInfo)
		for j, proto := range workload.Tasks {
			task := &Task{
				Schedule: proto.Schedule,
				Timeout: proto.Timeout,
			}
			// N.B.: Can't assign inline, since it
			// references task itself
			task.Command = func(t time.Time) error {
				execCount := execCounts[task]
				exec := proto.Executions[execCount]
				time.Sleep(exec.Duration)
				execCounts[task] += 1
				return exec.Error
			}

			tasks[j] = task
			taskMap[task] = &proto
		}
		execMap := make(map[*Task][]*Execution)
		stallMap := make(map[*Task][]*Stall)

		start := time.Now()
		w := Watch(tasks...)

		done := make(chan bool)
		go drainExecutions(execMap, w.Executions(), done)
		go drainStalls(stallMap, w.Stalls(), done)

		duration := workload.WatchDuration
		if duration == 0 {
			// Figure out duration if one was not provided
			for _, proto := range workload.Tasks {
				// pad each task by half its schedule
				currDuration := proto.Schedule / 2
				for _, exec := range proto.Executions {
					currDuration += exec.Duration
				}
				if currDuration > duration {
					duration = currDuration
				}
			}
		}
		<- time.After(duration)
		w.Stop()
		<- done
		<- done

		for task, actual := range execCounts {
			proto := taskMap[task]
			if expected := len(proto.Executions); expected != actual {
				t.Errorf("workload %d task %v: expected %d invocations; got %d",
					i, task, expected, actual)
			}
		}

		for task, execs := range execMap {
			proto := taskMap[task]
			if expected, actual := len(proto.Executions), len(execs); expected != actual {
				t.Errorf("workload %d task %v: expected %d executions; got %d",
					i, task, expected, actual)
			}
			for j, exec := range execs {
				if exec.Task != task {
					t.Errorf("workload %d task %v: expected execution %d task reference to match; got %v",
						i, task, j, exec.Task)
				}
				slack := 1 * time.Millisecond
				stepDelay := time.Duration(j + 1) * task.Schedule
				if expected := start.Add(stepDelay); !within(expected, exec.StartedAt, slack) {
					t.Errorf("workload %d task %v: expected execution %d start to be within %v of schedule; got within %v",
						i, task, j, slack, exec.StartedAt.Sub(expected))
				}
				if exec.FinishedAt.Before(exec.StartedAt) {
					t.Errorf("workload %d task %v: expected execution %d not to shear time; started at %v, finished at %v",
						i, task, j, exec.StartedAt, exec.FinishedAt)
				}

				if expected, duration := proto.Executions[j].Duration, exec.FinishedAt.Sub(exec.StartedAt); duration > expected + slack || duration < expected - slack {
					t.Errorf("workload %d task %v: expected execution %d to run for %v±%v; got %v",
						i, task, j, expected, slack, exec.FinishedAt.Sub(exec.StartedAt))
				}
				if expected := proto.Executions[j].Error; exec.Error != expected {
					t.Errorf("workload %d task %v: expected execution %d error to be %v; got %v",
						i, task, j, expected, exec.Error)
				}
			}
		}
		for task, stalls := range stallMap {
			proto := taskMap[task]
			expectedStalls := 0
			for _, exec := range proto.Executions {
				if exec.Duration > proto.Timeout {
					expectedStalls += 1
				}
			}
			if stallCount := len(stalls); stallCount != expectedStalls {
				t.Errorf("workload %d task %v: expected %v stalls; got %d",
					i, task, expectedStalls, stallCount)
			}
		}
		// Now let's sleep for another cycle to make sure we
		// don't have any stray goroutines that will cause
		// panics after task execution stops
		maxFreq := 0 * time.Millisecond
		for _, task := range workload.Tasks {
			if task.Schedule > maxFreq {
				maxFreq = task.Schedule
			}
		}
		<- time.After(maxFreq)
	}
}


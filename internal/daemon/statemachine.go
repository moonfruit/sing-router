// Package daemon 包含 supervisor、状态机、HTTP API 与子进程编排。
package daemon

import (
	"fmt"
	"sync"
)

// State 是 daemon 状态机的枚举。
type State int

const (
	StateBooting State = iota
	StateRunning
	StateReloading
	StateDegraded
	StateStopping
	StateStopped
	StateFatal
)

func (s State) String() string {
	switch s {
	case StateBooting:
		return "booting"
	case StateRunning:
		return "running"
	case StateReloading:
		return "reloading"
	case StateDegraded:
		return "degraded"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// StateMachine 串行化状态转移；不内含异步行为。
type StateMachine struct {
	mu      sync.Mutex
	current State
}

// NewStateMachine 初始 booting。
func NewStateMachine() *StateMachine { return &StateMachine{current: StateBooting} }

// Current 返回当前 state。
func (s *StateMachine) Current() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// allowed 描述合法的 (from→to) 关系。
var allowed = map[State]map[State]bool{
	StateBooting:   {StateRunning: true, StateFatal: true, StateStopping: true},
	StateRunning:   {StateReloading: true, StateDegraded: true, StateStopping: true},
	StateReloading: {StateRunning: true, StateFatal: true, StateStopping: true},
	StateDegraded:  {StateRunning: true, StateStopping: true},
	StateStopping:  {StateStopped: true, StateFatal: true, StateBooting: true /* shutdown 中途取消极少见，但保留可能 */},
	StateStopped:   {StateBooting: true, StateStopping: true},
	StateFatal:     {StateStopping: true},
}

// Transition 切换状态；非法转移返回 error，不改变当前 state。
func (s *StateMachine) Transition(to State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	transitions, ok := allowed[s.current]
	if !ok {
		return fmt.Errorf("no transitions from %v", s.current)
	}
	if !transitions[to] {
		return fmt.Errorf("illegal transition %v → %v", s.current, to)
	}
	s.current = to
	return nil
}

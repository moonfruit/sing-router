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

// NewStateMachine 初始 stopped——daemon 还未调 Boot 时未启动。Boot 内部会先调
// Startup → transition(Booting)，stopped → booting 已在 allowed 表中。
func NewStateMachine() *StateMachine { return &StateMachine{current: StateStopped} }

// Current 返回当前 state。
func (s *StateMachine) Current() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// allowed 描述合法的 (from→to) 关系。
//
// 重启路径已被简化为「Shutdown + Startup」两段，没有独立 Reloading 态——
// 「正在重启」由 Supervisor.restartInFlight 字段承担（不入状态机），
// Restart 内部依次经过 Stopping → Stopped → Booting → Running。
//
// Fatal → Booting 仅供 RestartForce（Applier 失败 revert 后兜底）使用——
// 普通代码路径仍应把 Fatal 当作终态。
var allowed = map[State]map[State]bool{
	StateBooting:  {StateRunning: true, StateFatal: true, StateStopping: true},
	StateRunning:  {StateDegraded: true, StateStopping: true},
	StateDegraded: {StateStopping: true},
	StateStopping: {StateStopped: true, StateFatal: true},
	StateStopped:  {StateBooting: true},
	StateFatal:    {StateStopping: true, StateBooting: true},
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

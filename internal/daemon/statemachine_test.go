package daemon

import "testing"

func TestStateStrings(t *testing.T) {
	cases := map[State]string{
		StateBooting:  "booting",
		StateRunning:  "running",
		StateDegraded: "degraded",
		StateStopping: "stopping",
		StateStopped:  "stopped",
		StateFatal:    "fatal",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Fatalf("%v: want %s got %s", s, want, s.String())
		}
	}
}

func TestStateMachineInitialStopped(t *testing.T) {
	sm := NewStateMachine()
	if sm.Current() != StateStopped {
		t.Fatalf("initial: %v", sm.Current())
	}
}

// 重启已被简化为 Shutdown(Running→Stopping→Stopped) + Startup(Stopped→Booting→Running)。
// 没有独立 Reloading 态——「正在重启」由 Supervisor.restartInFlight 字段承担。
func TestStateMachineRestartHappyPath(t *testing.T) {
	sm := NewStateMachine()
	must := func(err error) {
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	// 首次 Boot: Stopped → Booting → Running
	must(sm.Transition(StateBooting))
	must(sm.Transition(StateRunning))
	// Restart 第一段 Shutdown: Running → Stopping → Stopped
	must(sm.Transition(StateStopping))
	must(sm.Transition(StateStopped))
	// Restart 第二段 Startup: Stopped → Booting → Running
	must(sm.Transition(StateBooting))
	must(sm.Transition(StateRunning))
	// Crash → Degraded → Shutdown → Startup
	must(sm.Transition(StateDegraded))
	must(sm.Transition(StateStopping))
	must(sm.Transition(StateStopped))
	must(sm.Transition(StateBooting))
	must(sm.Transition(StateRunning))
}

func TestStateMachineRejectsIllegalTransitions(t *testing.T) {
	sm := NewStateMachine()
	// stopped → running 直接走非法（应先经 booting）
	if err := sm.Transition(StateRunning); err == nil {
		t.Fatal("expected illegal transition error")
	}
}

func TestStateMachineFatalIsTerminal(t *testing.T) {
	sm := NewStateMachine()
	if err := sm.Transition(StateBooting); err != nil {
		t.Fatalf("stopped→booting: %v", err)
	}
	if err := sm.Transition(StateFatal); err != nil {
		t.Fatalf("booting→fatal should be ok: %v", err)
	}
	// fatal → running 直接走仍非法
	if err := sm.Transition(StateRunning); err == nil {
		t.Fatal("fatal→running should be illegal")
	}
	// fatal → stopping 允许（手动 Stop 兜底）
	if err := sm.Transition(StateStopping); err != nil {
		t.Fatalf("fatal→stopping should be ok: %v", err)
	}
}

// Fatal → Booting 仅供 Supervisor.RestartForce（Applier 失败 revert 后兜底）使用——
// 普通代码路径仍应把 Fatal 视为终态。这条用例锁住 RestartForce 的兜底通路存在。
func TestStateMachineFatalToBootingForRestartForce(t *testing.T) {
	sm := NewStateMachine()
	if err := sm.Transition(StateBooting); err != nil {
		t.Fatalf("stopped→booting: %v", err)
	}
	if err := sm.Transition(StateFatal); err != nil {
		t.Fatalf("booting→fatal: %v", err)
	}
	if err := sm.Transition(StateBooting); err != nil {
		t.Fatalf("fatal→booting should be ok for RestartForce: %v", err)
	}
	if err := sm.Transition(StateRunning); err != nil {
		t.Fatalf("booting→running: %v", err)
	}
}

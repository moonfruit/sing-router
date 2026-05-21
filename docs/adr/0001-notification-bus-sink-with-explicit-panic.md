# 通知子系统采用 bus-sink 订阅 + panic 显式同步的混合接入

通知子系统主要作为 `clef.Bus` 的订阅者(`notify.Sink`)接入,复用 `EmitterStack` 的 `Attach`/drain 机制——daemon 业务代码一行不改,通知纯粹是 wireup + config 层的事,契合项目"与 sing-box 解耦"的一贯风格。唯独 panic 走显式同步调用(`reportPanic` 直接调 `Notifier.NotifySync`),因为进程濒死时不能依赖异步队列排空。

## Considered Options

- **全显式 `Notify()` 调用**:被否。会把通知逻辑散落到 supervisor / applier / sync 各处、与既有 clef 事件重复,且在 panic recover 路径里做显式调用脆弱。

## Consequences

bus-sink 只能通知**已经被发射**的 clef 事件。子进程崩溃/恢复等当前总线上无对应事件的状态变化,必须先往 daemon 补发新的 clef 事件(`supervisor.child.crashed` / `supervisor.recovered` / `supervisor.crash.unrecovered` / `daemon.started` / `daemon.stopped`)。未捕获的 Go runtime fatal 既不进总线也不在显式路径,无法通知——这是已知缺口。

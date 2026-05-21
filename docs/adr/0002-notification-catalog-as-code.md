# Kind 目录是静态 Go 代码,而非配置驱动模板

EventID→Notification 的目录(每个 Kind 的标题措辞、正文渲染逻辑、Priority)写死在 Go 代码里;`daemon.toml` 只提供 `enabled` / `min_priority` / `disabled_kinds` 等开关,不提供文案模板。因为每种事件携带的字段不同(`apply.ok` 带 zoo/rule/bin/cn 布尔、`sync.item.updated` 带 Name/Version、`supervisor.child.crashed` 带 CrashCount),把结构化字段渲染成可读正文本就需要按 Kind 写代码;在 TOML 里写模板既丑陋又无法优雅访问这些结构化字段。

## Consequences

用户能开关单个 Kind、能调 Priority 阈值,但不能新增或重写通知文案;新增一个"可通知的事件"需要改代码(往目录加一个 Kind)。

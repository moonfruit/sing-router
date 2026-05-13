package log

// ConnColorizer 给 ConnectionId 分配显眼颜色：同一 ID 在缓存窗口内拿同色；窗口
// 大小等于调色板大小，超出按 LRU 淘汰，回收颜色给新 ID。profile=none 时所有
// 接口为 no-op。
//
// 不是并发安全（logs 命令单 goroutine 渲染，无需加锁）。
type ConnColorizer struct {
	palette []string
	cap     int

	// mru[0] 是最久未见的；mru[len-1] 是最新的。
	mru   []string
	used  map[string]int // id → palette idx
	freed []int          // 可复用的 palette idx（淘汰回收后存入）
}

// NewConnColorizer 构造一个 colorizer。palette 为空时（profile=none）返回的实例
// Wrap 直接原样返回输入。
func NewConnColorizer(profile Profile) *ConnColorizer {
	pal := ConnPalette(profile)
	c := &ConnColorizer{
		palette: pal,
		cap:     len(pal),
	}
	if c.cap > 0 {
		c.mru = make([]string, 0, c.cap)
		c.used = make(map[string]int, c.cap)
		c.freed = make([]int, 0, c.cap)
		for i := c.cap - 1; i >= 0; i-- {
			c.freed = append(c.freed, i)
		}
	}
	return c
}

// Wrap 返回 ANSI 包裹的 id；profile=none 直接返回 id。空 id 也直接返回。
func (c *ConnColorizer) Wrap(id string) string {
	if c == nil || c.cap == 0 || id == "" {
		return id
	}
	idx, ok := c.used[id]
	if ok {
		c.touch(id)
	} else {
		idx = c.allocate(id)
	}
	return c.palette[idx] + id + ansiReset
}

func (c *ConnColorizer) allocate(id string) int {
	var idx int
	if n := len(c.freed); n > 0 {
		idx = c.freed[n-1]
		c.freed = c.freed[:n-1]
	} else {
		evict := c.mru[0]
		c.mru = c.mru[1:]
		idx = c.used[evict]
		delete(c.used, evict)
	}
	c.used[id] = idx
	c.mru = append(c.mru, id)
	return idx
}

func (c *ConnColorizer) touch(id string) {
	for i, v := range c.mru {
		if v == id {
			c.mru = append(c.mru[:i], c.mru[i+1:]...)
			c.mru = append(c.mru, id)
			return
		}
	}
}

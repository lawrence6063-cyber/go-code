package loop

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// defaultPollInterval 是 FSWatchTrigger 未配置 Debounce 时的轮询间隔。
const defaultPollInterval = 2 * time.Second

// TriggerSignal 携带一次触发的来源与可选载荷（如变更的文件、issue 号）。
type TriggerSignal struct {
	Source  string // "cron" | "fswatch" | "manual" | ...
	Payload string // 触发上下文，可拼进 Goal.Intent
}

// Trigger 是自治循环的触发源：被 cron / 文件监听 / 外部事件唤醒。
// 它把「何时该干活」与「干什么」（Goal）解耦。
type Trigger interface {
	// Fire 返回一个事件 channel；每次有元素到达即表示应触发一次目标循环。
	// ctx 取消即停止触发并关闭 channel（无 goroutine 泄漏）。
	Fire(ctx context.Context) (<-chan TriggerSignal, error)
}

// CronTrigger 按固定间隔触发（用 time.Ticker，不引第三方 cron 库）。
type CronTrigger struct {
	Interval time.Duration // 触发间隔；须为正
}

// Fire 见 Trigger 接口说明：按 Interval 周期投递 cron 信号，ctx 取消即收尾关闭 channel。
func (c CronTrigger) Fire(ctx context.Context) (<-chan TriggerSignal, error) {
	if c.Interval <= 0 {
		return nil, errors.New("cron interval must be positive")
	}
	out := make(chan TriggerSignal)
	go func() {
		defer close(out)
		ticker := time.NewTicker(c.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !sendSignal(ctx, out, TriggerSignal{Source: "cron"}) {
					return
				}
			}
		}
	}()
	return out, nil
}

// FSWatchTrigger 监听 workRoot 下的文件变更触发（零依赖：周期轮询最近修改时间 + 抖动合并）。
type FSWatchTrigger struct {
	WorkRoot string        // 监听根目录
	Debounce time.Duration // 轮询 / 抖动合并窗口；<=0 时取默认
}

// Fire 见 Trigger 接口说明：周期扫描 workRoot 最近修改时间，发现变更即投递 fswatch 信号。
func (w FSWatchTrigger) Fire(ctx context.Context) (<-chan TriggerSignal, error) {
	if strings.TrimSpace(w.WorkRoot) == "" {
		return nil, errors.New("fswatch requires a work root")
	}
	poll := w.Debounce
	if poll <= 0 {
		poll = defaultPollInterval
	}
	out := make(chan TriggerSignal)
	go func() {
		defer close(out)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		last := latestModTime(w.WorkRoot)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if cur := latestModTime(w.WorkRoot); cur.After(last) {
					last = cur
					if !sendSignal(ctx, out, TriggerSignal{Source: "fswatch", Payload: w.WorkRoot}) {
						return
					}
				}
			}
		}
	}()
	return out, nil
}

// sendSignal 在尊重 ctx 取消的前提下投递一个触发信号；返回 false 表示已取消。
func sendSignal(ctx context.Context, out chan<- TriggerSignal, sig TriggerSignal) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- sig:
		return true
	}
}

// latestModTime 遍历 root 取最近修改时间，跳过点目录与 data 目录（避免自身写入触发循环）。
func latestModTime(root string) time.Time {
	var latest time.Time
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 容错：跳过不可访问项
		}
		if d.IsDir() && path != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if info, e := d.Info(); e == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest
}

// skipDir 报告监听时应跳过的目录（点目录如 .git/.cogent、运行期 data 目录）。
func skipDir(name string) bool {
	return name == "data" || strings.HasPrefix(name, ".")
}

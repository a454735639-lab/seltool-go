package main

import (
	"crypto/subtle"
	"sync"
	"time"
)

// 企业微信回调的防重放。
//
// 回调签名 = SHA1(token, timestamp, nonce, encrypt)，其中 timestamp 由调用方
// 传入且服务端此前完全不校验，签名也就永久有效：抓到一次合法的 POST（明文
// HTTP、反代 access_log、中间人）就能无限重放「暂停定时」「站点导入」
// 「全部重试」这类高危指令。
//
// 这里补两道：
//  1. 时间戳窗口（默认 ±5 分钟）；
//  2. MsgId 去重（企业微信每条消息带唯一 MsgId），保留一个窗口周期。

const (
	wecomReplayWindow = 5 * time.Minute
	wecomReplayMax    = 2048
)

// sigEqual 用常量时间比较签名，避免 `!=` 的逐字节短路带来的时序侧信道。
func sigEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type replayGuard struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	sweep int
}

func newReplayGuard() *replayGuard {
	return &replayGuard{seen: map[string]time.Time{}}
}

// Check 返回 true 表示这条消息可以处理。
// createTime 为消息自带的秒级时间戳；msgId 为消息唯一 ID。
// 两者都缺失时（理论上不会发生）放行但由调用方记日志。
func (g *replayGuard) Check(createTime int64, msgId string) bool {
	now := time.Now()
	if createTime > 0 {
		ts := time.Unix(createTime, 0)
		if d := now.Sub(ts); d > wecomReplayWindow || d < -wecomReplayWindow {
			return false
		}
	}
	if msgId == "" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.seen[msgId]; dup {
		return false
	}
	g.sweep++
	if g.sweep >= 256 || len(g.seen) >= wecomReplayMax {
		g.sweep = 0
		for k, t := range g.seen {
			if now.Sub(t) > wecomReplayWindow*2 {
				delete(g.seen, k)
			}
		}
		// 仍然过多就整体清空（宁可让窗口短暂变窄，也不让内存涨上去）。
		if len(g.seen) >= wecomReplayMax {
			g.seen = map[string]time.Time{}
		}
	}
	g.seen[msgId] = now
	return true
}

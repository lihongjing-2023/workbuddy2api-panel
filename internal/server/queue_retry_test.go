package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatQueueWaitingNoRotate429 6020 带 data.retry_after 时：
// 网关内部短等一次、拿同一个号重试一次（不换号），仍 busy 才对外 429 + Retry-After=40。
func TestChatQueueWaitingNoRotate429(t *testing.T) {
	const body = `{"code":6020,"msg":"queue.waiting.title","requestId":"01a181ca3b67934d01c3f64599c8490c","data":{"queue_position":2502,"queue_size":"999+","retry_after":40,"estimated_wait":12510}}`
	var calls int32
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return http.StatusTooManyRequests, body, false
	})
	h := NewHandler(Config{
		Pool: testPoolWith(
			&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999},
			&auth.Auth{UID: "q2", AccessToken: "at-q2", ExpiresAt: 9999999999},
		),
		Upstream:  up,
		MaxRotate: 3,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))

	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("上游调用 %d 次，want 2（内部等一次后同号重试一次，仍 busy 才对外返回）", n)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429 (body=%s)", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "40" {
		t.Errorf("Retry-After=%q want 40（按上游给的时长睡，别用本地兜底）", ra)
	}
	if !strings.Contains(rec.Body.String(), "upstream_busy") {
		t.Errorf("body 应保留 upstream_busy 语义: %s", rec.Body)
	}
}

// TestChatQueueFullStill503 6021 无 retry_after：保持 503，不臆造 Retry-After。
func TestChatQueueFullStill503(t *testing.T) {
	const body = `{"code":6021,"msg":"queue.full.title","requestId":"b1bc87ea3a771e835e388328713d47cf"}`
	var calls int32
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return http.StatusTooManyRequests, body, false
	})
	h := NewHandler(Config{
		Pool:      testPoolWith(&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999}),
		Upstream:  up,
		MaxRotate: 3,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503 (body=%s)", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "" {
		t.Errorf("6021 无上游明示时长，不应带 Retry-After，实际 %q", ra)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("上游调用 %d 次，want 2（内部等一次后同号重试一次）", n)
	}
}

// TestChatQueueBusyRetrySucceeds 上游第一次 busy、第二次成功时，客户端应拿到 200，
// 且两次都打在同一个号上（前缀缓存不丢）。
func TestChatQueueBusyRetrySucceeds(t *testing.T) {
	const busy = `{"code":6021,"msg":"queue.full.title","requestId":"b1bc87ea3a771e835e388328713d47cf"}`
	var calls int32
	var auths []string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		auths = append(auths, authz)
		if atomic.LoadInt32(&calls) == 1 {
			return http.StatusTooManyRequests, busy, false
		}
		return 200, sseOK, true
	})
	h := NewHandler(Config{
		Pool:      testPoolWith(&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999}),
		Upstream:  up,
		MaxRotate: 3,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (body=%s)", rec.Code, rec.Body)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("上游调用 %d 次，want 2", n)
	}
	if len(auths) == 2 && auths[0] != auths[1] {
		t.Errorf("内部重试换号了：%q -> %q，want 同一个号", auths[0], auths[1])
	}
}

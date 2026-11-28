package upstream

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixedUsageRand(roll int) usageRand {
	return func(n int) int {
		if n == 100 {
			return roll
		}
		// jitter(5) 取 11 个候选，返回中点即零偏移。
		if n == 11 {
			return 5
		}
		return 0
	}
}

func TestRollCacheHitRewriteBuckets(t *testing.T) {
	cfg := CacheHitRewriteConfig{
		Enabled:       true,
		JitterPercent: 5,
		Buckets: []CacheHitRewriteBucket{
			{Probability: 30, HitPercent: 30},
			{Probability: 30, HitPercent: 80},
		},
	}

	cases := []struct {
		name   string
		roll   int
		want   int
		wantOK bool
	}{
		{"first bucket", 0, 30, true},
		{"second bucket", 30, 80, true},
		{"no rewrite", 60, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RollCacheHitRewrite(cfg, fixedUsageRand(tc.roll))
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("RollCacheHitRewrite() = %d/%v, want %d/%v", got, ok, tc.want, tc.wantOK)
			}
		})
	}

	disabled := cfg
	disabled.Enabled = false
	if _, ok := RollCacheHitRewrite(disabled, fixedUsageRand(0)); ok {
		t.Fatal("disabled config must not rewrite")
	}
}

func TestRollCacheHitRewriteJitterMovesTarget(t *testing.T) {
	cfg := CacheHitRewriteConfig{
		Enabled:       true,
		JitterPercent: 5,
		Buckets:       []CacheHitRewriteBucket{{Probability: 100, HitPercent: 30}},
	}
	// roll=0 必进唯一档；jitter 取上端（10 → +5）后命中目标为 35。
	rnd := func(n int) int {
		if n == 100 {
			return 0
		}
		if n == 11 {
			return 10
		}
		return 0
	}
	got, ok := RollCacheHitRewrite(cfg, rnd)
	if !ok || got != 35 {
		t.Fatalf("jittered target=%d ok=%v, want 35/true", got, ok)
	}
}

func TestApplyCacheHitRewriteAliasesAndRounding(t *testing.T) {
	usage := map[string]any{
		"prompt_tokens":            1000.0,
		"completion_tokens":        5.0,
		"total_tokens":             1005.0,
		"credit":                   0.1,
		"prompt_cache_hit_tokens":  200.0,
		"prompt_cache_miss_tokens": 800.0,
		"prompt_tokens_details":    map[string]any{"cached_tokens": 200.0},
		"input_tokens_details":     map[string]any{"cached_tokens": 200.0},
	}
	out := ApplyCacheHitRewrite(usage, 30)
	wantHit, wantMiss := 300.0, 700.0
	for _, key := range []string{"prompt_cache_hit_tokens", "cache_read_input_tokens", "cached_tokens"} {
		if got := out[key]; got != wantHit {
			t.Errorf("%s=%v want %v", key, got, wantHit)
		}
	}
	if got := out["prompt_cache_miss_tokens"]; got != wantMiss {
		t.Errorf("prompt_cache_miss_tokens=%v want %v", got, wantMiss)
	}
	for _, section := range []string{"prompt_tokens_details", "input_tokens_details"} {
		d := out[section].(map[string]any)
		if got := d["cached_tokens"]; got != wantHit {
			t.Errorf("%s.cached_tokens=%v want %v", section, got, wantHit)
		}
	}
	if out["total_tokens"] != 1005.0 || out["credit"] != 0.1 {
		t.Errorf("non-cache fields changed: %#v", out)
	}
	// 入参不能被修改。
	if usage["prompt_cache_hit_tokens"] != 200.0 {
		t.Error("input usage was mutated")
	}

	// 1001 * 80% = 800.8，四舍五入到 801，miss = 200。
	rounded := map[string]any{
		"prompt_tokens":            1001.0,
		"prompt_cache_hit_tokens":  0.0,
		"prompt_cache_miss_tokens": 1001.0,
	}
	out2 := ApplyCacheHitRewrite(rounded, 80)
	if got := out2["prompt_cache_hit_tokens"]; got != 801.0 {
		t.Errorf("rounded hit=%v want 801", got)
	}
	if got := out2["prompt_cache_miss_tokens"]; got != 200.0 {
		t.Errorf("rounded miss=%v want 200", got)
	}
}

func TestApplyCacheHitRewriteSkipsWhenNoCacheDimension(t *testing.T) {
	usage := map[string]any{"prompt_tokens": 1000.0, "completion_tokens": 5.0}
	if got := ApplyCacheHitRewrite(usage, 80); got["prompt_cache_hit_tokens"] != nil {
		t.Fatalf("no-cache usage should be untouched, got %#v", got)
	}
	missing := map[string]any{"prompt_cache_hit_tokens": 10.0}
	got := ApplyCacheHitRewrite(missing, 80)
	if len(got) != 1 || got["prompt_cache_hit_tokens"] != 10.0 {
		t.Fatalf("missing prompt_tokens should be untouched, got %#v", got)
	}
}

func TestCacheHitRewriteConfigNormalize(t *testing.T) {
	cfg := CacheHitRewriteConfig{
		Enabled:       true,
		JitterPercent: 99,
		Buckets: []CacheHitRewriteBucket{
			{Probability: -5, HitPercent: 120},
			{Probability: 200, HitPercent: -3},
		},
	}
	cfg.Normalize()
	if cfg.JitterPercent != 50 {
		t.Fatalf("jitter=%d want 50", cfg.JitterPercent)
	}
	if cfg.Buckets[0].Probability != 0 || cfg.Buckets[0].HitPercent != 100 {
		t.Errorf("bucket0=%+v want 0/100", cfg.Buckets[0])
	}
	if cfg.Buckets[1].Probability != 100 || cfg.Buckets[1].HitPercent != 0 {
		t.Errorf("bucket1=%+v want 100/0", cfg.Buckets[1])
	}
}

func TestStreamHintRewritesClientUsage(t *testing.T) {
	raw := `data: {"id":"x1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":5,"prompt_cache_hit_tokens":200,"prompt_cache_miss_tokens":800,"prompt_tokens_details":{"cached_tokens":200}}}` + "\n\n" +
		"data: [DONE]\n\n"

	cfg := CacheHitRewriteConfig{
		Enabled:       true,
		JitterPercent: 0,
		Buckets:       []CacheHitRewriteBucket{{Probability: 100, HitPercent: 80}},
	}
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil, WithUsageCacheHitRewrite(cfg)); err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	for _, ln := range strings.Split(rec.Body.String(), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "data: ") && !strings.Contains(ln, "[DONE]") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(ln, "data: ")), &frame); err != nil {
				t.Fatal(err)
			}
		}
	}
	if frame == nil {
		t.Fatal("no data frame found")
	}
	u := frame["usage"].(map[string]any)
	if got := u["prompt_tokens_details"].(map[string]any)["cached_tokens"]; got != 800.0 {
		t.Fatalf("stream cached_tokens=%v want 800", got)
	}
	if got := u["prompt_cache_miss_tokens"]; got != 200.0 {
		t.Fatalf("stream miss=%v want 200", got)
	}
}

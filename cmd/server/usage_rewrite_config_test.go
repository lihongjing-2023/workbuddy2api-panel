package main

import "testing"

func TestDefaultUsageCacheHitRewrite(t *testing.T) {
	c := Default()
	cfg := c.Features.UsageCacheHitRewrite
	if cfg.Enabled {
		t.Fatal("usage cache hit rewrite must be disabled by default")
	}
	if cfg.JitterPercent != 5 {
		t.Fatalf("default jitter=%d want 5", cfg.JitterPercent)
	}
	if len(cfg.Buckets) != 2 || cfg.Buckets[0].Probability != 30 || cfg.Buckets[0].HitPercent != 30 ||
		cfg.Buckets[1].Probability != 30 || cfg.Buckets[1].HitPercent != 80 {
		t.Fatalf("default buckets=%+v want 30/30 and 30/80", cfg.Buckets)
	}
}

func TestParseUsageCacheHitRewriteConfig(t *testing.T) {
	raw := []byte(`{"features":{"usage_cache_hit_rewrite":{"enabled":true,"jitter_percent":0,"buckets":[{"probability":45,"hit_percent":55}]}}}`)
	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Features.UsageCacheHitRewrite
	if !cfg.Enabled || cfg.JitterPercent != 0 || len(cfg.Buckets) != 1 {
		t.Fatalf("parsed config=%+v", cfg)
	}
	if cfg.Buckets[0].Probability != 45 || cfg.Buckets[0].HitPercent != 55 {
		t.Fatalf("parsed bucket=%+v want 45/55", cfg.Buckets[0])
	}
}

func TestUsageCacheHitRewriteConfigNormalizeClamps(t *testing.T) {
	raw := []byte(`{"features":{"usage_cache_hit_rewrite":{"enabled":true,"jitter_percent":200,"buckets":[{"probability":-3,"hit_percent":101}]}}}`)
	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Features.UsageCacheHitRewrite
	if cfg.JitterPercent != 50 {
		t.Fatalf("jitter=%d want 50", cfg.JitterPercent)
	}
	if cfg.Buckets[0].Probability != 0 || cfg.Buckets[0].HitPercent != 100 {
		t.Fatalf("bucket=%+v want 0/100", cfg.Buckets[0])
	}
}

func TestUsageCacheHitRewriteEnvOverride(t *testing.T) {
	t.Setenv("WB2A_USAGE_CACHE_HIT_REWRITE", "true")
	t.Setenv("WB2A_USAGE_CACHE_HIT_REWRITE_JITTER", "7")
	c := Default()
	applyEnv(c)
	if !c.Features.UsageCacheHitRewrite.Enabled || c.Features.UsageCacheHitRewrite.JitterPercent != 7 {
		t.Fatalf("env override failed: %+v", c.Features.UsageCacheHitRewrite)
	}
}

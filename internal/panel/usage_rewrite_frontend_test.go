package panel

import (
	"strings"
	"testing"
)

func TestUsageCacheHitRewriteConfigUI(t *testing.T) {
	html := string(indexHTML)
	for _, name := range []string{
		"usage_cache_hit_rewrite_enabled",
		"usage_cache_hit_rewrite_jitter",
		"usage_cache_hit_rewrite_buckets",
	} {
		if !strings.Contains(html, `name="`+name+`"`) {
			t.Errorf("index.html missing config field %q", name)
		}
	}

	js := string(appJS)
	for _, needle := range []string{
		"usage_cache_hit_rewrite_enabled: ['features', 'usage_cache_hit_rewrite', 'enabled']",
		"name === 'usage_cache_hit_rewrite_buckets'",
		"JSON.parse(raw)",
	} {
		if !strings.Contains(js, needle) {
			t.Errorf("app.js missing %q", needle)
		}
	}
}

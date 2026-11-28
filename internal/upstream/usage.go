package upstream

import (
	"encoding/json"
	"math"
	"math/rand/v2"
)

// normalizeUsageCacheAliases keeps cache-hit aliases consistent before the
// response leaves the gateway. Some WorkBuddy responses carry the real hit in
// prompt_tokens_details.cached_tokens while also emitting
// cache_read_input_tokens: 0 and cached_tokens: 0 compatibility aliases.
// Strict downstream parsers may prefer those zero aliases and lose the hit.
func normalizeUsageCacheAliases(usage map[string]any) map[string]any {
	best, ok := bestUsageCacheHitTokens(usage)
	if !ok || best <= 0 {
		return usage
	}

	out := cloneUsageMap(usage)
	out["cache_read_input_tokens"] = best
	out["cached_tokens"] = best
	out["prompt_cache_hit_tokens"] = best

	promptDetails := cloneUsageDetails(out, "prompt_tokens_details")
	promptDetails["cached_tokens"] = best
	out["prompt_tokens_details"] = promptDetails

	// Responses API consumers use this nested form. Preserve it when the
	// upstream already supplies it, but do not invent it for Chat-only clients.
	if _, exists := out["input_tokens_details"]; exists {
		inputDetails := cloneUsageDetails(out, "input_tokens_details")
		inputDetails["cached_tokens"] = best
		out["input_tokens_details"] = inputDetails
	}

	return out
}

func bestUsageCacheHitTokens(usage map[string]any) (float64, bool) {
	paths := []struct {
		section string
		key     string
	}{
		{"prompt_tokens_details", "cached_tokens"},
		{"", "prompt_cache_hit_tokens"},
		{"", "cache_read_input_tokens"},
		{"", "cached_tokens"},
		{"input_tokens_details", "cached_tokens"},
	}

	for _, path := range paths {
		var value any
		if path.section == "" {
			value = usage[path.key]
		} else if details, ok := usage[path.section].(map[string]any); ok {
			value = details[path.key]
		}
		if tokens, ok := positiveUsageNumber(value); ok {
			return tokens, true
		}
	}
	return 0, false
}

func positiveUsageNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, n > 0
	case float32:
		value := float64(n)
		return value, value > 0
	case int:
		return float64(n), n > 0
	case int64:
		return float64(n), n > 0
	case int32:
		return float64(n), n > 0
	case uint:
		return float64(n), n > 0
	case uint64:
		return float64(n), n > 0
	case uint32:
		return float64(n), n > 0
	default:
		return 0, false
	}
}

func cloneUsageMap(usage map[string]any) map[string]any {
	out := make(map[string]any, len(usage))
	for key, value := range usage {
		out[key] = value
	}
	return out
}

func cloneUsageDetails(usage map[string]any, key string) map[string]any {
	out := make(map[string]any)
	details, _ := usage[key].(map[string]any)
	for detailKey, value := range details {
		out[detailKey] = value
	}
	return out
}

// UsagePromptTokens 返回 usage 顶层的 prompt_tokens；缺失或非数值时 ok=false。
func UsagePromptTokens(usage map[string]any) (float64, bool) {
	if usage == nil {
		return 0, false
	}
	return usageNumber(usage, "prompt_tokens")
}

// UsageCacheHitTokens 返回 usage 里的缓存命中 token 数（多别名取最优，口径与
// 回写给客户端的 normalizeUsageCacheAliases 一致）。供网关统计层（usage 桶 /
// reqlog）观测命中率使用；usage 缺失该维度时 ok=false。
func UsageCacheHitTokens(usage map[string]any) (float64, bool) {
	if usage == nil {
		return 0, false
	}
	return bestUsageCacheHitTokens(usage)
}

// UsageCacheMissTokens 返回 usage 里的缓存未命中 token 数：优先读上游显式的
// prompt_cache_miss_tokens，缺失时按 prompt_tokens - 命中 推导（推导值为负时
// 视为不可信，返回 ok=false）。
func UsageCacheMissTokens(usage map[string]any) (float64, bool) {
	if usage == nil {
		return 0, false
	}
	if miss, ok := usageNumber(usage, "prompt_cache_miss_tokens"); ok {
		return miss, true
	}
	prompt, okP := usageNumber(usage, "prompt_tokens")
	hit, okH := UsageCacheHitTokens(usage)
	if okP && okH && prompt-hit >= 0 {
		return prompt - hit, true
	}
	return 0, false
}

// usageNumber 从 usage 顶层取数值字段（JSON 数字可能是 float64 / json.Number 形态）。
func usageNumber(usage map[string]any, key string) (float64, bool) {
	v, ok := usage[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// CacheHitRewriteBucket 一个缓存命中重写档位：probability 是进入该档的整数概率
// (0-100)，hit_percent 是命中 token 占 prompt_tokens 的整数百分比 (0-100)。
// 各档概率之和 <=100，余量即「不重写」的概率。
type CacheHitRewriteBucket struct {
	Probability int `json:"probability"`
	HitPercent  int `json:"hit_percent"`
}

// CacheHitRewriteConfig 回程 usage 缓存命中值的概率重写配置。
//
// JitterPercent 对**每档概率**与**命中百分比**都加 [-j, +j] 的整数扰动，让固定
// 档位不明显；0 表示关闭扰动。配置本身不决定是否生效，Enabled=false 时整条链路
// 零抽样、零改写。
type CacheHitRewriteConfig struct {
	Enabled       bool                    `json:"enabled"`
	JitterPercent int                     `json:"jitter_percent"`
	Buckets       []CacheHitRewriteBucket `json:"buckets"`
}

// Clone 返回深层副本，供 atomic 快照持有：调用方之后改配置不会污染已发布的快照。
func (c CacheHitRewriteConfig) Clone() CacheHitRewriteConfig {
	out := c
	if c.Buckets != nil {
		out.Buckets = append([]CacheHitRewriteBucket(nil), c.Buckets...)
	}
	return out
}

// Normalize 把非法值钳到安全区间；不报错，避免一个百分比笔误让网关起不来。
// buckets 上限 8 档，足够实际使用且防止误配超长列表放大每次请求的抽样成本。
func (c *CacheHitRewriteConfig) Normalize() {
	if c.JitterPercent < 0 {
		c.JitterPercent = 0
	}
	if c.JitterPercent > 50 {
		c.JitterPercent = 50
	}
	if len(c.Buckets) > 8 {
		c.Buckets = c.Buckets[:8]
	}
	for i := range c.Buckets {
		if c.Buckets[i].Probability < 0 {
			c.Buckets[i].Probability = 0
		}
		if c.Buckets[i].Probability > 100 {
			c.Buckets[i].Probability = 100
		}
		if c.Buckets[i].HitPercent < 0 {
			c.Buckets[i].HitPercent = 0
		}
		if c.Buckets[i].HitPercent > 100 {
			c.Buckets[i].HitPercent = 100
		}
	}
}

// usageRand 是 math/rand/v2.IntN 的形状；测试可注入确定性实现。
type usageRand func(n int) int

func defaultUsageRand(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.IntN(n)
}

// jitterPercent 给 base 加 [-j, +j] 的整数扰动并钳回 [0,100]。
func jitterPercent(base, jitter int, rnd usageRand) int {
	if jitter <= 0 {
		return clampPercent(base)
	}
	// 2*j+1 保证每个偏移等概率，且 j=0 时不会调用 rnd(0)。
	v := base + rnd(2*jitter+1) - jitter
	return clampPercent(v)
}

func clampPercent(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// RollCacheHitRewrite 为一次响应抽样一个目标命中百分比。ok=false 表示本轮不重写
// （配置关闭、没有可用档位，或随机落在「不重写」余量里）。流式请求只调用一次，
// 保证同一条 SSE 的所有 usage 帧使用同一个目标。
func RollCacheHitRewrite(cfg CacheHitRewriteConfig, rnd usageRand) (targetPercent int, ok bool) {
	if !cfg.Enabled || len(cfg.Buckets) == 0 {
		return 0, false
	}
	if rnd == nil {
		rnd = defaultUsageRand
	}
	// 逐档扰动概率并累加，剩余权重即不重写。超出的部分截断，保证总和 <=100。
	remaining := 100
	weights := make([]int, len(cfg.Buckets))
	for i, b := range cfg.Buckets {
		w := jitterPercent(b.Probability, cfg.JitterPercent, rnd)
		if w > remaining {
			w = remaining
		}
		weights[i] = w
		remaining -= w
	}
	if remaining == 100 {
		return 0, false
	}
	r := rnd(100)
	cum := 0
	for i, w := range weights {
		cum += w
		if r < cum {
			return jitterPercent(cfg.Buckets[i].HitPercent, cfg.JitterPercent, rnd), true
		}
	}
	return 0, false
}

// ApplyCacheHitRewrite 把 targetPercent 应用到 usage 的所有缓存别名上。
// 只改已有缓存维度的 usage；上游完全没回缓存字段时不凭空造。prompt_tokens 缺失
// 或非正数同样原样返回。返回新 map，不修改入参。
func ApplyCacheHitRewrite(usage map[string]any, targetPercent int) map[string]any {
	if usage == nil {
		return usage
	}
	if _, okHit := UsageCacheHitTokens(usage); !okHit {
		if _, okMiss := UsageCacheMissTokens(usage); !okMiss {
			return usage
		}
	}
	prompt, okPrompt := UsagePromptTokens(usage)
	if !okPrompt || prompt <= 0 {
		return usage
	}
	target := clampPercent(targetPercent)
	hit := int64(math.Round(prompt * float64(target) / 100))
	if max := int64(prompt); hit > max {
		hit = max
	}
	if hit < 0 {
		hit = 0
	}
	miss := int64(prompt) - hit

	out := cloneUsageMap(usage)
	// 用 float64 落值：现有 normalizeUsageCacheAliases 与 Responses 侧
	// usagesObject 都按 float64 读，写 int64 会让 Responses usage 丢命中值。
	out["prompt_cache_hit_tokens"] = float64(hit)
	out["cache_read_input_tokens"] = float64(hit)
	out["cached_tokens"] = float64(hit)
	out["prompt_cache_miss_tokens"] = float64(miss)

	promptDetails := cloneUsageDetails(out, "prompt_tokens_details")
	promptDetails["cached_tokens"] = float64(hit)
	out["prompt_tokens_details"] = promptDetails
	if _, exists := out["input_tokens_details"]; exists {
		inputDetails := cloneUsageDetails(out, "input_tokens_details")
		inputDetails["cached_tokens"] = float64(hit)
		out["input_tokens_details"] = inputDetails
	}
	return out
}

// RewriteUsageCacheHit 是 Roll + Apply 的便捷入口，供非流式响应在统计完成后调用。
func RewriteUsageCacheHit(usage map[string]any, cfg CacheHitRewriteConfig, rnd usageRand) map[string]any {
	target, ok := RollCacheHitRewrite(cfg, rnd)
	if !ok {
		return usage
	}
	return ApplyCacheHitRewrite(usage, target)
}

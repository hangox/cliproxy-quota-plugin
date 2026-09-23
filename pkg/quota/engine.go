package quota

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"
)

// AccountLister 抽象账号查询接口，便于解耦与单元测试 mock。
type AccountLister interface {
	ListAccounts(ctx context.Context) ([]*AuthAccount, error)
}

type cachedQuotaEntry struct {
	result   ProviderQuotaResult
	cachedAt time.Time
}

// QuotaEngine 是通用配额策略引擎。
type QuotaEngine struct {
	accountLister AccountLister
	strategies    map[string]ProviderStrategy
	mu            sync.RWMutex
	cacheTTL      time.Duration
	cache         map[string]cachedQuotaEntry
	cacheMu       sync.RWMutex
}

// NewQuotaEngine 创建一个新的配额引擎实例，默认开启 15 秒短时缓存防止探活穿透。
func NewQuotaEngine(lister AccountLister) *QuotaEngine {
	return &QuotaEngine{
		accountLister: lister,
		strategies:    make(map[string]ProviderStrategy),
		cacheTTL:      15 * time.Second,
		cache:         make(map[string]cachedQuotaEntry),
	}
}

// SetCacheTTL 设置短时缓存过期时间（设置为 0 则禁用缓存）。
func (e *QuotaEngine) SetCacheTTL(d time.Duration) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.cacheTTL = d
}

// ClearCache 清空缓存。
func (e *QuotaEngine) ClearCache() {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.cache = make(map[string]cachedQuotaEntry)
}

// SetDefaultProxyURL 设置并同步所有支持出站代理策略的默认代理地址。
func (e *QuotaEngine) SetDefaultProxyURL(proxyURL string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, s := range e.strategies {
		if pc, ok := s.(interface{ SetDefaultProxyURL(string) }); ok {
			pc.SetDefaultProxyURL(proxyURL)
		}
	}
}

// Register 注册指定 Provider 的配额采集策略。
func (e *QuotaEngine) Register(strategy ProviderStrategy) {
	if strategy == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.strategies[strings.ToLower(strings.TrimSpace(strategy.Provider()))] = strategy
}

func (e *QuotaEngine) getStrategy(provider string) (ProviderStrategy, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.strategies[strings.ToLower(strings.TrimSpace(provider))]
	return s, ok
}

type weightedAverage struct {
	sum       float64
	weightSum float64
}

func (w *weightedAverage) add(value float64, weight int64) {
	if weight <= 0 {
		return
	}
	w.sum += value * float64(weight)
	w.weightSum += float64(weight)
}

func (w weightedAverage) average() (float64, bool) {
	if w.weightSum <= 0 {
		return 0, false
	}
	return w.sum / w.weightSum, true
}

type windowAggregate struct {
	values   weightedAverage
	earliest time.Time
}

func (a *windowAggregate) add(pct float64, weight int64, resetAt time.Time) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	a.values.add(pct, weight)
	if !resetAt.IsZero() && (a.earliest.IsZero() || resetAt.Before(a.earliest)) {
		a.earliest = resetAt
	}
}

func (a *windowAggregate) toWindow(name string) (QuotaWindow, bool) {
	avg, ok := a.values.average()
	if !ok {
		return QuotaWindow{}, false
	}
	rounded := math.Round(avg*100) / 100
	win := QuotaWindow{
		Name:                name,
		RemainingPercentage: &rounded,
		Available:           rounded > 0,
	}
	if !a.earliest.IsZero() {
		win.ResetsAt = &a.earliest
	}
	return win, true
}

type groupAggregate struct {
	fiveHour windowAggregate
	weekly   windowAggregate
}

func (g *groupAggregate) add(bucket RawBucket, weight int64) {
	kind := strings.ToLower(strings.TrimSpace(bucket.Kind))
	switch {
	case strings.Contains(kind, "hour") || strings.Contains(kind, "5h"):
		g.fiveHour.add(bucket.RemainingPercent, weight, bucket.ResetAt)
	case strings.Contains(kind, "week") || strings.Contains(kind, "7d") || strings.Contains(kind, "7 day"):
		g.weekly.add(bucket.RemainingPercent, weight, bucket.ResetAt)
	}
}

func (g *groupAggregate) toWindows() []QuotaWindow {
	wins := make([]QuotaWindow, 0, 2)
	if w, ok := g.fiveHour.toWindow("5h"); ok {
		wins = append(wins, w)
	}
	if w, ok := g.weekly.toWindow("7d"); ok {
		wins = append(wins, w)
	}
	return wins
}

type accountFetchResult struct {
	account *AuthAccount
	data    AccountQuotaData
	err     error
}

// CollectProvider 聚合单个 Provider 的多账号加权配额（支持短时缓存与并发采集）。
func (e *QuotaEngine) CollectProvider(ctx context.Context, provider string) (ProviderQuotaResult, error) {
	providerKey := strings.ToLower(strings.TrimSpace(provider))

	// 1. 检查短时缓存
	e.cacheMu.RLock()
	if e.cacheTTL > 0 {
		if entry, found := e.cache[providerKey]; found && time.Since(entry.cachedAt) < e.cacheTTL {
			e.cacheMu.RUnlock()
			return entry.result, nil
		}
	}
	e.cacheMu.RUnlock()

	strategy, ok := e.getStrategy(providerKey)
	if !ok {
		return ProviderQuotaResult{Provider: providerKey}, nil
	}

	if e.accountLister == nil {
		return ProviderQuotaResult{Provider: providerKey}, nil
	}

	accounts, errList := e.accountLister.ListAccounts(ctx)
	if errList != nil {
		return ProviderQuotaResult{Provider: providerKey}, errList
	}

	var totalAccounts int
	var eligibleAccounts []*AuthAccount

	for _, account := range accounts {
		if account == nil || !strings.EqualFold(strings.TrimSpace(account.Provider), providerKey) {
			continue
		}
		totalAccounts++

		// 账号有效性过滤（剔除 Disabled 以及 StatusDisabled / StatusError）
		if account.Disabled ||
			strings.EqualFold(strings.TrimSpace(account.Status), "disabled") ||
			strings.EqualFold(strings.TrimSpace(account.Status), "error") {
			continue
		}

		// 账号权重过滤（<=0 排除池化）
		if account.Weight <= 0 {
			continue
		}

		eligibleAccounts = append(eligibleAccounts, account)
	}

	activeAccounts := len(eligibleAccounts)

	// 2. 并发采集有效账号配额
	fetchResults := make([]accountFetchResult, activeAccounts)
	var wg sync.WaitGroup
	for i, acc := range eligibleAccounts {
		wg.Add(1)
		go func(idx int, a *AuthAccount) {
			defer wg.Done()
			d, errFetch := strategy.FetchAccountQuota(ctx, a)
			fetchResults[idx] = accountFetchResult{
				account: a,
				data:    d,
				err:     errFetch,
			}
		}(i, acc)
	}
	wg.Wait()

	// 3. 在主协程无锁顺序汇总聚合结果（严格无竞态）
	globalAgg := &groupAggregate{}
	groupAggs := make(map[string]*groupAggregate)
	totalResetCards := 0
	hasResetCards := false

	for _, r := range fetchResults {
		if r.err != nil {
			// 单账号采集失败优雅降级，不阻断全盘池化
			continue
		}

		weight := r.account.Weight

		if r.data.ResetCards > 0 {
			totalResetCards += r.data.ResetCards
			hasResetCards = true
		}

		// 累加顶层 buckets（全局通用桶，如 Codex）
		for _, b := range r.data.Buckets {
			globalAgg.add(b, weight)
		}

		// 累加各模型独立 groups buckets（如 Antigravity 的 gemini 与 claude_gpt 彼此独立）
		for grpName, buckets := range r.data.Groups {
			grpKey := strings.ToLower(strings.TrimSpace(grpName))
			gAgg, exists := groupAggs[grpKey]
			if !exists {
				gAgg = &groupAggregate{}
				groupAggs[grpKey] = gAgg
			}
			for _, b := range buckets {
				gAgg.add(b, weight)
			}
		}
	}

	res := ProviderQuotaResult{
		Provider:       providerKey,
		TotalAccounts:  totalAccounts,
		ActiveAccounts: activeAccounts,
		Windows:        globalAgg.toWindows(),
	}

	if len(groupAggs) > 0 {
		groups := make(map[string]QuotaGroup, len(groupAggs))
		for k, gAgg := range groupAggs {
			wins := gAgg.toWindows()
			if len(wins) > 0 {
				groups[k] = QuotaGroup{Windows: wins}
			}
		}
		if len(groups) > 0 {
			res.Groups = groups
		}
	}

	if hasResetCards {
		res.ResetCards = &totalResetCards
	}

	// 4. 写回短时缓存
	if e.cacheTTL > 0 {
		e.cacheMu.Lock()
		e.cache[providerKey] = cachedQuotaEntry{
			result:   res,
			cachedAt: time.Now(),
		}
		e.cacheMu.Unlock()
	}

	return res, nil
}

// CollectAll 并发或批量采集所有已注册 Provider 的配额数据。
func (e *QuotaEngine) CollectAll(ctx context.Context) (UnifiedQuotaResponse, error) {
	e.mu.RLock()
	providers := make([]string, 0, len(e.strategies))
	for p := range e.strategies {
		providers = append(providers, p)
	}
	e.mu.RUnlock()

	results := make(map[string]ProviderQuotaResult, len(providers))
	for _, p := range providers {
		res, err := e.CollectProvider(ctx, p)
		if err == nil {
			results[p] = res
		}
	}

	return UnifiedQuotaResponse{
		UpdatedAt: time.Now(),
		Providers: results,
	}, nil
}

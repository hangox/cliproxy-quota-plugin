package quota

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type mockAccountLister struct {
	accounts []*AuthAccount
	err      error
}

func (m *mockAccountLister) ListAccounts(_ context.Context) ([]*AuthAccount, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.accounts, nil
}

type mockStrategy struct {
	provider  string
	quotaMap  map[string]AccountQuotaData
	errMap    map[string]error
	callCount int64
}

func (m *mockStrategy) Provider() string {
	return m.provider
}

func (m *mockStrategy) FetchAccountQuota(_ context.Context, account *AuthAccount) (AccountQuotaData, error) {
	atomic.AddInt64(&m.callCount, 1)
	if m.errMap != nil {
		if err, ok := m.errMap[account.ID]; ok && err != nil {
			return AccountQuotaData{}, err
		}
	}
	return m.quotaMap[account.ID], nil
}

func TestQuotaEngineWeightedPoolingAndFiltering(t *testing.T) {
	resetAt1 := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	resetAt2 := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC) // 较早的 reset 时间

	strategy := &mockStrategy{
		provider: "testprovider",
		quotaMap: map[string]AccountQuotaData{
			"acc-light": {
				AuthID: "acc-light",
				Buckets: []RawBucket{
					{Kind: "5h", RemainingPercent: 25.0, ResetAt: resetAt1},
					{Kind: "7d", RemainingPercent: 25.0, ResetAt: resetAt1},
				},
				ResetCards: 2,
			},
			"acc-heavy": {
				AuthID: "acc-heavy",
				Buckets: []RawBucket{
					{Kind: "5h", RemainingPercent: 75.0, ResetAt: resetAt2},
					{Kind: "7d", RemainingPercent: 75.0, ResetAt: resetAt2},
				},
				ResetCards: 3,
			},
			"acc-disabled": {
				AuthID: "acc-disabled",
				Buckets: []RawBucket{
					{Kind: "5h", RemainingPercent: 100.0},
				},
				ResetCards: 10,
			},
			"acc-zero-weight": {
				AuthID: "acc-zero-weight",
				Buckets: []RawBucket{
					{Kind: "5h", RemainingPercent: 0.0},
				},
			},
		},
	}

	accounts := []*AuthAccount{
		{
			ID:       "acc-light",
			Provider: "testprovider",
			Status:   "active",
			Weight:   1,
		},
		{
			ID:       "acc-heavy",
			Provider: "testprovider",
			Status:   "active",
			Weight:   3,
		},
		{
			ID:       "acc-disabled",
			Provider: "testprovider",
			Disabled: true,
			Weight:   5,
		},
		{
			ID:       "acc-zero-weight",
			Provider: "testprovider",
			Status:   "active",
			Weight:   0,
		},
		{
			ID:       "acc-other-provider",
			Provider: "other",
			Status:   "active",
			Weight:   1,
		},
	}

	lister := &mockAccountLister{accounts: accounts}
	engine := NewQuotaEngine(lister)
	engine.Register(strategy)

	res, err := engine.CollectProvider(context.Background(), "testprovider")
	if err != nil {
		t.Fatalf("CollectProvider error: %v", err)
	}

	// 1. 验证账号统计
	if res.TotalAccounts != 4 {
		t.Errorf("TotalAccounts = %d, want 4", res.TotalAccounts)
	}
	if res.ActiveAccounts != 2 {
		t.Errorf("ActiveAccounts = %d, want 2", res.ActiveAccounts)
	}

	// 2. 验证加权池化计算：(25*1 + 75*3) / (1 + 3) = 250 / 4 = 62.5
	if len(res.Windows) != 2 {
		t.Fatalf("len(Windows) = %d, want 2", len(res.Windows))
	}
	win5h := res.Windows[0]
	if win5h.Name != "5h" || win5h.RemainingPercentage == nil || *win5h.RemainingPercentage != 62.5 {
		t.Errorf("5h window = %+v, want 62.5%%", win5h)
	}
	if win5h.ResetsAt == nil || !win5h.ResetsAt.Equal(resetAt2) {
		t.Errorf("5h resets_at = %v, want %v", win5h.ResetsAt, resetAt2)
	}

	win7d := res.Windows[1]
	if win7d.Name != "7d" || win7d.RemainingPercentage == nil || *win7d.RemainingPercentage != 62.5 {
		t.Errorf("7d window = %+v, want 62.5%%", win7d)
	}

	// 3. 验证 ResetCards 累加：2 + 3 = 5（禁用账号的 10 张被正确过滤）
	if res.ResetCards == nil || *res.ResetCards != 5 {
		t.Errorf("ResetCards = %v, want 5", res.ResetCards)
	}
}

func TestQuotaEngineMultiGroups(t *testing.T) {
	strategy := &mockStrategy{
		provider: "antigravity",
		quotaMap: map[string]AccountQuotaData{
			"acc-1": {
				AuthID: "acc-1",
				Groups: map[string][]RawBucket{
					"gemini": {
						{Kind: "5h", RemainingPercent: 80.0},
						{Kind: "7d", RemainingPercent: 60.0},
					},
					"claude_gpt": {
						{Kind: "5h", RemainingPercent: 90.0},
						{Kind: "7d", RemainingPercent: 40.0},
					},
				},
			},
			"acc-2": {
				AuthID: "acc-2",
				Groups: map[string][]RawBucket{
					"gemini": {
						{Kind: "5h", RemainingPercent: 100.0},
						{Kind: "7d", RemainingPercent: 80.0},
					},
					"claude_gpt": {
						{Kind: "5h", RemainingPercent: 100.0},
						{Kind: "7d", RemainingPercent: 60.0},
					},
				},
			},
		},
	}

	accounts := []*AuthAccount{
		{ID: "acc-1", Provider: "antigravity", Status: "active", Weight: 1},
		{ID: "acc-2", Provider: "antigravity", Status: "active", Weight: 1},
	}

	lister := &mockAccountLister{accounts: accounts}
	engine := NewQuotaEngine(lister)
	engine.Register(strategy)

	res, err := engine.CollectProvider(context.Background(), "antigravity")
	if err != nil {
		t.Fatalf("CollectProvider error: %v", err)
	}

	// 验证 groups 独立性（gemini 和 claude_gpt 严格隔离，不混入顶层 Windows）
	if len(res.Groups) != 2 {
		t.Fatalf("len(Groups) = %d, want 2", len(res.Groups))
	}
	geminiWins := res.Groups["gemini"].Windows
	if len(geminiWins) != 2 {
		t.Fatalf("gemini windows len = %d, want 2", len(geminiWins))
	}
	// gemini 5h: (80 + 100) / 2 = 90
	if *geminiWins[0].RemainingPercentage != 90 {
		t.Errorf("gemini 5h = %v, want 90", *geminiWins[0].RemainingPercentage)
	}
	// gemini 7d: (60 + 80) / 2 = 70
	if *geminiWins[1].RemainingPercentage != 70 {
		t.Errorf("gemini 7d = %v, want 70", *geminiWins[1].RemainingPercentage)
	}

	claudeWins := res.Groups["claude_gpt"].Windows
	// claude 5h: (90 + 100) / 2 = 95
	if *claudeWins[0].RemainingPercentage != 95 {
		t.Errorf("claude 5h = %v, want 95", *claudeWins[0].RemainingPercentage)
	}
	// claude 7d: (40 + 60) / 2 = 50
	if *claudeWins[1].RemainingPercentage != 50 {
		t.Errorf("claude 7d = %v, want 50", *claudeWins[1].RemainingPercentage)
	}

	// 顶层全局样本应保持为空，防止不同模型互相稀释
	if len(res.Windows) != 0 {
		t.Errorf("global windows should be empty for multi-group isolated models, got %d", len(res.Windows))
	}
}

func TestQuotaEngineErrorDegradation(t *testing.T) {
	strategy := &mockStrategy{
		provider: "testprovider",
		quotaMap: map[string]AccountQuotaData{
			"acc-healthy": {
				AuthID: "acc-healthy",
				Buckets: []RawBucket{
					{Kind: "5h", RemainingPercent: 80.0},
				},
				ResetCards: 1,
			},
		},
		errMap: map[string]error{
			"acc-failed": errors.New("network timeout"),
		},
	}

	accounts := []*AuthAccount{
		{ID: "acc-healthy", Provider: "testprovider", Status: "active", Weight: 1},
		{ID: "acc-failed", Provider: "testprovider", Status: "active", Weight: 1},
	}

	lister := &mockAccountLister{accounts: accounts}
	engine := NewQuotaEngine(lister)
	engine.Register(strategy)

	res, err := engine.CollectProvider(context.Background(), "testprovider")
	if err != nil {
		t.Fatalf("CollectProvider failed: %v", err)
	}

	// 异常账号不应导致整个池化失败，只影响聚合结果
	if res.TotalAccounts != 2 {
		t.Errorf("TotalAccounts = %d, want 2", res.TotalAccounts)
	}
	if res.ActiveAccounts != 2 {
		t.Errorf("ActiveAccounts = %d, want 2", res.ActiveAccounts)
	}
	if len(res.Windows) != 1 {
		t.Fatalf("len(Windows) = %d, want 1", len(res.Windows))
	}
	if *res.Windows[0].RemainingPercentage != 80.0 {
		t.Errorf("RemainingPercentage = %v, want 80.0", *res.Windows[0].RemainingPercentage)
	}
	if res.ResetCards == nil || *res.ResetCards != 1 {
		t.Errorf("ResetCards = %v, want 1", res.ResetCards)
	}
}

func TestQuotaEngineCacheHit(t *testing.T) {
	strategy := &mockStrategy{
		provider: "cached-prov",
		quotaMap: map[string]AccountQuotaData{
			"acc-1": {
				AuthID: "acc-1",
				Buckets: []RawBucket{
					{Kind: "5h", RemainingPercent: 50.0},
				},
			},
		},
	}

	accounts := []*AuthAccount{
		{ID: "acc-1", Provider: "cached-prov", Status: "active", Weight: 1},
	}

	lister := &mockAccountLister{accounts: accounts}
	engine := NewQuotaEngine(lister)
	engine.SetCacheTTL(10 * time.Second)
	engine.Register(strategy)

	// 第一次调用，触发底层采集
	res1, err1 := engine.CollectProvider(context.Background(), "cached-prov")
	if err1 != nil {
		t.Fatalf("CollectProvider 1 failed: %v", err1)
	}
	if atomic.LoadInt64(&strategy.callCount) != 1 {
		t.Fatalf("callCount = %d, want 1", strategy.callCount)
	}

	// 第二次调用，命中短时缓存，不触发底层 strategy
	res2, err2 := engine.CollectProvider(context.Background(), "cached-prov")
	if err2 != nil {
		t.Fatalf("CollectProvider 2 failed: %v", err2)
	}
	if atomic.LoadInt64(&strategy.callCount) != 1 {
		t.Errorf("callCount after cached call = %d, want 1", strategy.callCount)
	}
	if *res1.Windows[0].RemainingPercentage != *res2.Windows[0].RemainingPercentage {
		t.Errorf("cached result mismatch: %+v vs %+v", res1, res2)
	}

	// 清空缓存后再次调用，应重新触发采集
	engine.ClearCache()
	_, _ = engine.CollectProvider(context.Background(), "cached-prov")
	if atomic.LoadInt64(&strategy.callCount) != 2 {
		t.Errorf("callCount after cache clear = %d, want 2", strategy.callCount)
	}
}

func TestQuotaEngineConcurrency(t *testing.T) {
	numAccounts := 20
	quotaMap := make(map[string]AccountQuotaData, numAccounts)
	accounts := make([]*AuthAccount, numAccounts)

	for i := 0; i < numAccounts; i++ {
		id := fmt.Sprintf("acc-%d", i)
		accounts[i] = &AuthAccount{
			ID:       id,
			Provider: "conc-provider",
			Status:   "active",
			Weight:   1,
		}
		quotaMap[id] = AccountQuotaData{
			AuthID: id,
			Buckets: []RawBucket{
				{Kind: "5h", RemainingPercent: float64(50 + i)},
			},
		}
	}

	strategy := &mockStrategy{
		provider: "conc-provider",
		quotaMap: quotaMap,
	}

	lister := &mockAccountLister{accounts: accounts}
	engine := NewQuotaEngine(lister)
	engine.SetCacheTTL(0) // 禁用缓存测试纯并发
	engine.Register(strategy)

	res, err := engine.CollectProvider(context.Background(), "conc-provider")
	if err != nil {
		t.Fatalf("CollectProvider failed: %v", err)
	}

	if res.ActiveAccounts != numAccounts {
		t.Errorf("ActiveAccounts = %d, want %d", res.ActiveAccounts, numAccounts)
	}
	if len(res.Windows) != 1 {
		t.Fatalf("len(Windows) = %d, want 1", len(res.Windows))
	}
}

func TestQuotaEngineUnknownProviderAndCollectAll(t *testing.T) {
	engine := NewQuotaEngine(&mockAccountLister{})
	res, err := engine.CollectProvider(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("CollectProvider error: %v", err)
	}
	if res.Provider != "nonexistent" || res.TotalAccounts != 0 {
		t.Errorf("unexpected res for nonexistent provider: %+v", res)
	}

	all, err := engine.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("CollectAll error: %v", err)
	}
	if len(all.Providers) != 0 {
		t.Errorf("len(Providers) = %d, want 0", len(all.Providers))
	}
}

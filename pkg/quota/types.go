package quota

import (
	"context"
	"time"
)

// RawBucket 代表单个时间窗口的原始配额数据。
type RawBucket struct {
	Kind             string    `json:"kind"` // "5h" 或 "7d"
	RemainingPercent float64   `json:"remaining_percent"`
	ResetAt          time.Time `json:"reset_at,omitempty"`
}

// AccountQuotaData 代表单个账号获取到的配额数据。
type AccountQuotaData struct {
	AuthID     string                 `json:"auth_id"`
	Weight     int64                  `json:"weight"`
	Buckets    []RawBucket            `json:"buckets,omitempty"`
	Groups     map[string][]RawBucket `json:"groups,omitempty"`
	ResetCards int                    `json:"reset_cards,omitempty"`
}

// AuthAccount 代表从宿主获取到的统一账号信息结构。
type AuthAccount struct {
	ID         string            `json:"id"`
	AuthIndex  string            `json:"auth_index"`
	Name       string            `json:"name"`
	Provider   string            `json:"provider"`
	Status     string            `json:"status"`
	Disabled   bool              `json:"disabled"`
	Weight     int64             `json:"weight"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
}

// ProviderStrategy 定义各 Provider 配额采集策略接口。
type ProviderStrategy interface {
	Provider() string
	FetchAccountQuota(ctx context.Context, account *AuthAccount) (AccountQuotaData, error)
}

// QuotaWindow 代表聚合后的时间窗口指标。
type QuotaWindow struct {
	Name                string     `json:"name"`
	RemainingPercentage *float64   `json:"remaining_percentage,omitempty"`
	Available           bool       `json:"available"`
	ResetsAt            *time.Time `json:"resets_at,omitempty"`
}

// QuotaGroup 代表一个模型分组的窗口列表。
type QuotaGroup struct {
	Windows []QuotaWindow `json:"windows"`
}

// ProviderQuotaResult 代表单个 Provider 的池化聚合结果。
type ProviderQuotaResult struct {
	Provider       string                `json:"provider"`
	TotalAccounts  int                   `json:"total_accounts"`
	ActiveAccounts int                   `json:"active_accounts"`
	Windows        []QuotaWindow         `json:"windows"`
	Groups         map[string]QuotaGroup `json:"groups,omitempty"`
	ResetCards     *int                  `json:"reset_cards,omitempty"`
}

// UnifiedQuotaResponse 代表对外统一配额响应。
type UnifiedQuotaResponse struct {
	UpdatedAt time.Time                      `json:"updated_at"`
	Providers map[string]ProviderQuotaResult `json:"providers"`
}

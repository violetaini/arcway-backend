package notify

type EventType string

const (
	EventLogin            EventType = "login"
	EventSubscribeFetch   EventType = "subscribe_fetch"
	EventDailyTraffic     EventType = "daily_traffic"
	EventServerOffline    EventType = "server_offline"
	EventServerOnline     EventType = "server_online"
	EventTrafficThreshold EventType = "traffic_threshold"
	EventIPBan            EventType = "ip_ban"

	// Phase 2 新增 9 个事件
	EventTrafficThreshold80  EventType = "traffic_threshold_80"  // 用户流量达 80%(预警)
	EventOverLimit           EventType = "over_limit"            // 用户流量超 100%(已踢)
	EventPackageExpiring     EventType = "package_expiring"      // 套餐 N 天内到期
	EventPackageExpired      EventType = "package_expired"       // 套餐已到期(清理时点)
	EventUserRegistered      EventType = "user_registered"       // 新用户注册
	EventTelegramBound       EventType = "telegram_bound"        // 用户首次绑定 TG
	EventCertResult          EventType = "cert_result"           // 证书申请成功/失败
	EventAgentLongOffline    EventType = "agent_long_offline"    // agent 长期离线(N 分钟无心跳)
	EventDeviceLimitExceeded EventType = "device_limit_exceeded" // 用户触发设备数超限(agent 踢最旧)
)

type Config struct {
	Enabled                 bool
	BotToken                string
	ChatID                  string
	NotifyLogin             bool
	NotifySubscribeFetch    bool
	NotifyDailyTraffic      bool
	NotifyServerOffline     bool
	NotifyServerOnline      bool
	NotifyTrafficThreshold  bool
	DailyTrafficTime        string // "HH:MM"
	TrafficThresholdPercent int    // 0-100

	// Phase 2 新增 9 个开关 + 2 个参数
	NotifyTrafficThreshold80  bool
	NotifyOverLimit           bool
	NotifyPackageExpiring     bool
	PackageExpiringDaysAhead  int // 默认 3
	NotifyPackageExpired      bool
	NotifyUserRegistered      bool
	NotifyTelegramBound       bool
	NotifyCertResult          bool
	NotifyAgentLongOffline    bool
	AgentLongOfflineMinutes   int // 默认 30
	NotifyDeviceLimitExceeded bool
}

type Event struct {
	Type    EventType
	Title   string
	Message string
}

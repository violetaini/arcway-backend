package capabilities

// Feature identifies a backend capability that must also be advertised to agents.
type Feature string

const (
	FeatureEmbeddedXray Feature = "embedded"
	FeatureLimiter      Feature = "limiter"
	FeatureServerShare  Feature = "server_share"
	FeatureSpeedTest    Feature = "speed_test"

	// Unlimited is the legacy agent-protocol sentinel for an unbounded resource.
	Unlimited = 0
)

var localFeatures = []Feature{
	FeatureEmbeddedXray,
	FeatureLimiter,
	FeatureServerShare,
	FeatureSpeedTest,
}

// Manager exposes the capabilities compiled into this self-hosted build. It is
// deliberately stateless: capability availability never depends on a remote
// service, a machine fingerprint, a key, or persisted activation state.
type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

// HasFeature reports whether the local build contains a named capability.
func (*Manager) HasFeature(name Feature) bool {
	switch name {
	case FeatureEmbeddedXray, FeatureLimiter, FeatureServerShare, FeatureSpeedTest:
		return true
	default:
		return false
	}
}

// AgentPlan preserves the plan-shaped portion of the existing agent wire
// protocol. Zero resource maxima mean unlimited in this local policy.
type AgentPlan struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description,omitempty"`
	MaxServers  int      `json:"max_servers"`
	MaxNodes    int      `json:"max_nodes"`
	MaxUsers    int      `json:"max_users"`
	Features    []string `json:"features"`
}

// AgentStatus is sent using the legacy "license_status" message type because
// released agents already consume that wire name. It contains only local
// capability information and does not represent an external authorization.
type AgentStatus struct {
	Valid      bool       `json:"valid"`
	MaxServers int        `json:"max_servers"`
	Plan       *AgentPlan `json:"plan"`
}

func (*Manager) StatusForAgent() AgentStatus {
	features := make([]string, 0, len(localFeatures))
	for _, feature := range localFeatures {
		features = append(features, string(feature))
	}

	return AgentStatus{
		Valid:      true,
		MaxServers: Unlimited,
		Plan: &AgentPlan{
			Name:        "LOCAL",
			DisplayName: "本地全功能版",
			Description: "全部能力由本地构建提供",
			MaxServers:  Unlimited,
			MaxNodes:    Unlimited,
			MaxUsers:    Unlimited,
			Features:    features,
		},
	}
}

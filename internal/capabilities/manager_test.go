package capabilities

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestManagerEnablesAllLocalFeatures(t *testing.T) {
	manager := NewManager()
	for _, feature := range localFeatures {
		if !manager.HasFeature(feature) {
			t.Fatalf("expected feature %q to be enabled", feature)
		}
	}
	if manager.HasFeature(Feature("unknown")) {
		t.Fatal("unknown feature must not be reported as enabled")
	}
}

func TestStatusForAgentKeepsLegacyShapeWithUnlimitedQuotas(t *testing.T) {
	status := NewManager().StatusForAgent()
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal agent status: %v", err)
	}

	var decoded struct {
		Valid      bool `json:"valid"`
		MaxServers int  `json:"max_servers"`
		Plan       struct {
			Name       string   `json:"name"`
			MaxServers int      `json:"max_servers"`
			MaxNodes   int      `json:"max_nodes"`
			MaxUsers   int      `json:"max_users"`
			Features   []string `json:"features"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal agent status: %v", err)
	}

	wantFeatures := []string{"embedded", "limiter", "server_share", "speed_test"}
	if !decoded.Valid || decoded.Plan.Name != "LOCAL" {
		t.Fatalf("unexpected local status: %+v", decoded)
	}
	if decoded.MaxServers != Unlimited || decoded.Plan.MaxServers != Unlimited ||
		decoded.Plan.MaxNodes != Unlimited || decoded.Plan.MaxUsers != Unlimited {
		t.Fatalf("resource quotas are not unlimited: %+v", decoded)
	}
	if !reflect.DeepEqual(decoded.Plan.Features, wantFeatures) {
		t.Fatalf("unexpected feature list: got %v want %v", decoded.Plan.Features, wantFeatures)
	}
}

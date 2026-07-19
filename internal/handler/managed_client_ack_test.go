package handler

import "testing"

func TestValidateAgentClientMutation(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantRestart bool
		wantError   bool
	}{
		{name: "applied", body: `{"success":true,"message":"client added"}`},
		{name: "runtime deferred", body: `{"success":true,"runtime_warning":"runtime apply failed"}`, wantRestart: true},
		{name: "legacy no-op", body: `{"success":true,"message":"client already present (no-op)"}`, wantRestart: true},
		{name: "explicit unchanged", body: `{"success":true,"changed":false}`, wantRestart: true},
		{name: "explicit changed", body: `{"success":true,"changed":true}`},
		{name: "negative ACK", body: `{"success":false}`, wantError: true},
		{name: "invalid ACK", body: `not-json`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restart, err := validateAgentClientMutation([]byte(test.body))
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %v", err, test.wantError)
			}
			if restart != test.wantRestart {
				t.Fatalf("restart = %v, want %v", restart, test.wantRestart)
			}
		})
	}
}

func TestValidateAgentBatchItemResult(t *testing.T) {
	tests := []struct {
		result    string
		wantNoOp  bool
		wantError bool
	}{
		{result: "ok"},
		{result: "ok (no-op)", wantNoOp: true},
		{result: "err: inbound missing", wantError: true},
		{result: "applied", wantError: true},
		{result: "", wantError: true},
	}
	for _, test := range tests {
		noOp, err := validateAgentBatchItemResult(test.result)
		if noOp != test.wantNoOp || (err != nil) != test.wantError {
			t.Fatalf("result %q: noOp=%v err=%v", test.result, noOp, err)
		}
	}
}

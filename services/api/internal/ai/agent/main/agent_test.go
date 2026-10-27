package mainagent

import "testing"

func TestNewAgentRejectsMissingToolRegistry(t *testing.T) {
	agent, err := NewAgent(&mainTestModel{}, "test", 8192, nil)
	if err == nil || agent != nil {
		t.Fatal("agent without tools was constructed", agent, err)
	}
}

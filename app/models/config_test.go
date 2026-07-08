package models

import (
	"reflect"
	"testing"
)

func TestRememberAgentID(t *testing.T) {
	c := &Config{}
	c.RememberAgentID("agent-a")
	c.RememberAgentID("agent-b")
	c.RememberAgentID("agent-a") // 重複不再加入
	c.RememberAgentID("")        // 空字串不加入
	want := []string{"agent-a", "agent-b"}
	if !reflect.DeepEqual(c.KnownAgentIDs, want) {
		t.Errorf("KnownAgentIDs = %v, want %v", c.KnownAgentIDs, want)
	}
}

func TestAgentIDOptions_IncludesCurrent(t *testing.T) {
	c := &Config{AgentID: "agent-x", KnownAgentIDs: []string{"agent-a"}}
	want := []string{"agent-a", "agent-x"}
	if got := c.AgentIDOptions(); !reflect.DeepEqual(got, want) {
		t.Errorf("options = %v, want %v", got, want)
	}

	// 目前值已在清單中 → 不重複
	c = &Config{AgentID: "agent-a", KnownAgentIDs: []string{"agent-a", "agent-b"}}
	want = []string{"agent-a", "agent-b"}
	if got := c.AgentIDOptions(); !reflect.DeepEqual(got, want) {
		t.Errorf("options = %v, want %v", got, want)
	}

	// 空 AgentID → 只回清單
	c = &Config{KnownAgentIDs: []string{"agent-a"}}
	want = []string{"agent-a"}
	if got := c.AgentIDOptions(); !reflect.DeepEqual(got, want) {
		t.Errorf("options = %v, want %v", got, want)
	}
}

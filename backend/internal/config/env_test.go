package config

import "testing"

func TestServeWebEnvironmentOverride(t *testing.T) {
	t.Setenv("RAG_SERVER_SERVE_WEB", "false")
	cfg := Default()
	applyEnv(cfg)
	if cfg.WebEnabled() {
		t.Fatal("serve_web=false environment override ignored")
	}
}

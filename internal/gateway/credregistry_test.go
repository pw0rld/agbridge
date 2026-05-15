package gateway

import (
	"sync"
	"testing"

	"github.com/pw0rld/agbridge/internal/config"
)

func sampleCfg() *config.GatewayConfig {
	return &config.GatewayConfig{
		Agents: []config.AgentEntry{
			{Name: "alpha", APIKeyHash: "sha256:a", AllowedDaemons: []string{"lab1"}},
			{Name: "bravo", APIKeyHash: "sha256:b", AllowedDaemons: []string{"lab2"}},
		},
		Daemons: []config.DaemonEntry{
			{Name: "lab1", TokenHash: "sha256:x"},
			{Name: "lab2", TokenHash: "sha256:y"},
		},
	}
}

func TestCredRegistryLookups(t *testing.T) {
	cr := NewCredRegistry(sampleCfg())
	if a, ok := cr.LookupAgent("alpha"); !ok || a.APIKeyHash != "sha256:a" {
		t.Errorf("LookupAgent(alpha) = (%+v, %v)", a, ok)
	}
	if _, ok := cr.LookupAgent("ghost"); ok {
		t.Errorf("LookupAgent(ghost) unexpectedly ok")
	}
	if d, ok := cr.LookupDaemon("lab1"); !ok || d.TokenHash != "sha256:x" {
		t.Errorf("LookupDaemon(lab1) = (%+v, %v)", d, ok)
	}
}

func TestCredRegistryReplace(t *testing.T) {
	cr := NewCredRegistry(sampleCfg())
	newCfg := &config.GatewayConfig{
		Agents: []config.AgentEntry{
			{Name: "alpha", APIKeyHash: "sha256:rotated", AllowedDaemons: []string{"lab1"}},
			// bravo removed
		},
		Daemons: []config.DaemonEntry{
			{Name: "lab1", TokenHash: "sha256:x"},
		},
	}
	cr.Replace(newCfg)
	a, ok := cr.LookupAgent("alpha")
	if !ok || a.APIKeyHash != "sha256:rotated" {
		t.Errorf("after Replace, LookupAgent(alpha) = (%+v, %v)", a, ok)
	}
	if _, ok := cr.LookupAgent("bravo"); ok {
		t.Errorf("after Replace, bravo should be gone")
	}
	if _, ok := cr.LookupDaemon("lab2"); ok {
		t.Errorf("after Replace, lab2 should be gone")
	}
}

func TestCredRegistryConcurrentRWClean(t *testing.T) {
	cr := NewCredRegistry(sampleCfg())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cr.LookupAgent("alpha")
					cr.LookupDaemon("lab1")
				}
			}
		}()
	}
	go func() {
		for i := 0; i < 100; i++ {
			cr.Replace(sampleCfg())
		}
		close(stop)
	}()
	wg.Wait()
}

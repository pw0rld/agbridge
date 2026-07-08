package main

import (
	"testing"

	"github.com/pw0rld/agbridge/internal/gateway"
)

func TestBuildKnownDaemonsMultiTarget(t *testing.T) {
	resp := gateway.EnrollResponse{
		Targets: []gateway.EnrollTarget{
			{Name: "lab01", DaemonPubkey: "pub01"},
			{Name: "lab02", DaemonPubkey: "pub02"},
		},
	}
	known, def := buildKnownDaemons(resp)
	if len(known) != 2 {
		t.Fatalf("want 2 known daemons, got %d: %+v", len(known), known)
	}
	if known["lab01"].Pubkey != "pub01" || known["lab02"].Pubkey != "pub02" {
		t.Fatalf("pubkeys wrong: %+v", known)
	}
	if def != "lab01" {
		t.Fatalf("default target = %q, want lab01", def)
	}
}

func TestBuildKnownDaemonsLegacySingleTarget(t *testing.T) {
	resp := gateway.EnrollResponse{TargetDaemon: "lab01", DaemonPubkey: "pub01"}
	known, def := buildKnownDaemons(resp)
	if len(known) != 1 || known["lab01"].Pubkey != "pub01" {
		t.Fatalf("legacy single-target not built: %+v", known)
	}
	if def != "lab01" {
		t.Fatalf("default target = %q, want lab01", def)
	}
}

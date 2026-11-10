package handler

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func TestCanonicalCardIssuerAliases(t *testing.T) {
	cases := map[string]string{
		"ch1": "one", "CH1": "one", "1": "one", "one": "one",
		"ch3": "three", "channel3": "three",
		"ch4": "four", "4": "four",
		"unknown": "", "": "",
	}
	for in, want := range cases {
		if got := cardplatform.CanonicalCardIssuer(in); got != want {
			t.Fatalf("CanonicalCardIssuer(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBuildSelectPrioritySkipsOfflineAndDisabled(t *testing.T) {
	rules := []db.CardSelectionRule{
		{PlanKey: "OFFLINE", Channel: "ch1", Enabled: true},
		{PlanKey: "DISABLED", Channel: "four", Enabled: false},
		{PlanKey: "LIVE", Channel: "ch4", Enabled: true},
		{PlanKey: "LIVE", Channel: "four", Enabled: true},
	}
	products := []db.CardProductCache{
		{ProductCode: "OFFLINE", Issuer: "one", Enabled: false},
		{ProductCode: "DISABLED", Issuer: "four", Enabled: true},
		{ProductCode: "LIVE", Issuer: "four", Enabled: true},
	}
	got := buildSelectPriority(rules, products)
	if len(got) != 1 || got[0].SegmentKey != "LIVE" || got[0].Issuer != "four" {
		t.Fatalf("want only LIVE/four, got %+v", got)
	}
}

func TestFirstUsablePrefSkipsPolicyProductIfOffline(t *testing.T) {
	policy := SiteRedeemPolicy{ProductCode: "OFFLINE", Issuer: "one"}
	rules := []db.CardSelectionRule{
		{PlanKey: "LIVE", Channel: "ch4", Enabled: true},
	}
	products := []db.CardProductCache{
		{ProductCode: "OFFLINE", Issuer: "one", Enabled: false},
		{ProductCode: "LIVE", Issuer: "four", Enabled: true},
	}
	iss, typ, key := firstUsableCardPref(policy, rules, products)
	if iss != "four" || typ != "product" || key != "LIVE" {
		t.Fatalf("got %s/%s/%s", iss, typ, key)
	}
}

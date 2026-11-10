package provider

import "testing"

func TestMaxIssueCountMatchesBatchLimit(t *testing.T) {
	if MaxIssueCount != 200 {
		t.Fatalf("MaxIssueCount=%d want 200", MaxIssueCount)
	}
}

func TestXPremiumPlans(t *testing.T) {
	for _, plan := range []string{"plus_monthly", "premium_yearly", "super_monthly", "ultra_yearly", "x_premium_monthly"} {
		if !IsXPremiumPlan(plan) {
			t.Fatal(plan)
		}
	}
	for _, plan := range []string{"basic_monthly", "premium_plus_yearly"} {
		if IsXPremiumPlan(plan) {
			t.Fatal("retired X tier still accepted: " + plan)
		}
	}
	if IsXPremiumPlan("plus") || IsXPremiumPlan("grok_plus_monthly") {
		t.Fatal("cross product plan")
	}
}

func TestGrokPlans(t *testing.T) {
	for _, p := range []string{"plus_monthly", "super_yearly", "ultra_monthly", "grok_premium_monthly", "grok_premium_yearly",
		"monthly", "yearly", "lite_monthly", "heavy_monthly", "grok_heavy_yearly"} { // 后五个是改名前的旧键
		if !IsGrokPlan(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"plus", "pro_20x", "premium_monthly", "premium_plus_monthly", "pro_monthly", "pro_plus_monthly"} {
		if IsGrokPlan(p) {
			t.Fatal("cross product plan", p)
		}
	}
}

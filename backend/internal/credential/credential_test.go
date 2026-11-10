package credential

import "testing"

func TestXCredentialsAreNotInvoiceSessions(t *testing.T) {
	if !IsXPremiumCredential(`{"auth_token":"fixture_auth_token_000"}`) {
		t.Fatal("X auth_token JSON credential rejected")
	}
	if !IsXPremiumCredential("6eac90ba41206ea277d1dd308d4b28c3f441094f") {
		t.Fatal("bare X auth_token rejected") // 2026-10-24 简化:裸 auth_token 也接受
	}
	if IsXPremiumCredential(`{"sessionToken":"fixture"}`) {
		t.Fatal("GPT credential misclassified")
	}
}

func TestGrokCredentials(t *testing.T) {
	if !IsGrokCredential("sso=abc.def") || !IsGrokCredential("sso-rw=x; sso=y") || IsGrokCredential(`{"sessionToken":"x"}`) || IsGrokCredential("abc") {
		t.Fatal("grok credential detection")
	}
}

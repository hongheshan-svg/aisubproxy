package paybrowser

import "testing"

func TestIsCFChallengePage(t *testing.T) {
	cases := []struct {
		p    ProbePage
		want bool
	}{
		{ProbePage{Title: "Just a moment..."}, true},
		{ProbePage{Title: "ChatGPT", Text: "Verify you are human by completing the action below."}, true},
		{ProbePage{Title: "ChatGPT", CFMarkup: true}, true},
		{ProbePage{Title: "ChatGPT", Text: "Log in  Sign up  What can I help with?"}, false},
	}
	for i, c := range cases {
		if got := IsCFChallengePage(c.p); got != c.want {
			t.Errorf("用例 %d: got %v want %v", i, got, c.want)
		}
	}
}

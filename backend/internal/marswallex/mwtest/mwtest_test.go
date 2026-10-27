package mwtest

import (
	"context"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const cardPath = "/v1/card/detail/c1"

func get(s *Server) error {
	_, err := s.Client().GetCard(context.Background(), "c1")
	return err
}

func TestOKReplyIsSuccess(t *testing.T) {
	s := New(t)
	s.On("GET", cardPath, OK(`{"card_id":"c1","card_no":"****1234","card_name":"x","status":"ACTIVE"}`))
	c, err := s.Client().GetCard(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "ACTIVE" {
		t.Errorf("status=%q", c.Status)
	}
	if n := len(s.Requests("GET", cardPath)); n != 1 {
		t.Errorf("requests=%d", n)
	}
}

func TestRejectedReplyIsRejected(t *testing.T) {
	s := New(t)
	s.On("GET", cardPath, Rejected(404, "ORDER_NOT_FOUND"))
	if got := marswallex.OutcomeOf(get(s)); got != marswallex.OutcomeRejected {
		t.Errorf("outcome=%v", got)
	}
}

func TestUnknownAndUnsignedAreUnknown(t *testing.T) {
	s := New(t)
	s.On("GET", cardPath, Unknown(), Reply{Unsigned: true})
	for i := 0; i < 2; i++ {
		if got := marswallex.OutcomeOf(get(s)); got != marswallex.OutcomeUnknown {
			t.Errorf("call %d outcome=%v", i, got)
		}
	}
}

func TestDropIsUnknown(t *testing.T) {
	s := New(t)
	s.On("GET", cardPath, Reply{Drop: true})
	if got := marswallex.OutcomeOf(get(s)); got != marswallex.OutcomeUnknown {
		t.Errorf("outcome=%v", got)
	}
}

func TestHangTimesOutAsUnknown(t *testing.T) {
	s := New(t)
	s.On("GET", cardPath, Reply{Hang: 3 * time.Second})
	if got := marswallex.OutcomeOf(get(s)); got != marswallex.OutcomeUnknown {
		t.Errorf("outcome=%v", got)
	}
}

func TestNotSentClient(t *testing.T) {
	s := New(t)
	_, err := s.NotSentClient().GetCard(context.Background(), "c1")
	if got := marswallex.OutcomeOf(err); got != marswallex.OutcomeNotSent {
		t.Errorf("outcome=%v err=%v", got, err)
	}
}

func TestQueueRepeatsLast(t *testing.T) {
	s := New(t)
	s.On("GET", cardPath,
		OK(`{"card_id":"c1","card_name":"a","status":"A"}`),
		OK(`{"card_id":"c1","card_name":"b","status":"B"}`))
	var got []string
	for i := 0; i < 3; i++ {
		c, err := s.Client().GetCard(context.Background(), "c1")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c.Status)
	}
	if got[0] != "A" || got[1] != "B" || got[2] != "B" {
		t.Errorf("got %v", got)
	}
}

func TestSignWebhookVerifies(t *testing.T) {
	s := New(t)
	pub, err := marswallex.ParsePublicKey(s.PlatformPublicKeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	body := []byte(`{"topic":"CARD_APPLY_RESULT","message_id":"m1","data":{}}`)
	if _, err := marswallex.ParseWebhook(s.SignWebhook(body, now), body, pub, now); err != nil {
		t.Fatal(err)
	}
}

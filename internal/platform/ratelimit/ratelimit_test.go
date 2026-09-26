package ratelimit_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/ratelimit"
)

const quote = "/kuepreorder.orders.v1.CheckoutService/QuoteOrder"

func TestLimiter_BurstThenRate(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC))
	l := ratelimit.New(clk, map[string]ratelimit.Rule{quote: {Every: time.Second, Burst: 3}})

	for i := range 3 {
		if !l.Allow(quote, "203.0.113.7") {
			t.Fatalf("call %d refused, want the burst of 3 allowed", i+1)
		}
	}
	if l.Allow(quote, "203.0.113.7") {
		t.Error("4th call allowed, want refused: the burst is spent")
	}
	if !l.Allow(quote, "198.51.100.9") {
		t.Error("another client refused, want each client counted apart")
	}
	clk.Advance(time.Second)
	if !l.Allow(quote, "203.0.113.7") || l.Allow(quote, "203.0.113.7") {
		t.Error("after a second, want exactly one more call")
	}
}

func TestLimiter_ProceduresWithoutRulesAreFree(t *testing.T) {
	l := ratelimit.New(clock.NewFake(time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)), nil)
	for range 100 {
		if !l.Allow("/kuepreorder.catalog.v1.StorefrontService/ListShopProducts", "203.0.113.7") {
			t.Fatal("refused a procedure without a rule")
		}
	}
}

// Buckets of clients long gone are dropped, and a returning client starts
// with a full bucket, as it would have anyway.
func TestLimiter_SweepsIdleBuckets(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC))
	l := ratelimit.New(clk, map[string]ratelimit.Rule{quote: {Every: time.Second, Burst: 2}})
	l.Allow(quote, "203.0.113.7")
	l.Allow(quote, "203.0.113.7")
	clk.Advance(time.Hour)
	for i := range 1100 { // enough calls to trigger a sweep
		l.Allow(quote, fmt.Sprintf("10.0.%d.%d", i/250, i%250))
	}
	for i := range 2 {
		if !l.Allow(quote, "203.0.113.7") {
			t.Errorf("call %d of a returning client after an hour refused, want a full bucket", i+1)
		}
	}
}

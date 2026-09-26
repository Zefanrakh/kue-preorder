// Package ratelimit limits how often one client may call a procedure
// (docs/architecture.md §22): a token bucket per procedure and client
// address, in memory. Each server instance keeps its own counts, which suits
// a single shop; Cloudflare in front adds a shared outer limit later.
package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/time/rate"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
)

// Rule allows a call every Every on average, and up to Burst at once.
type Rule struct {
	Every time.Duration
	Burst int
}

// sweepEvery is how many calls pass between sweeps of idle buckets.
const sweepEvery = 1024

type key struct{ procedure, client string }

type bucket struct {
	limiter *rate.Limiter
	idle    time.Duration // a full refill: after this long the bucket is as good as new
	last    time.Time
}

// Limiter enforces Rules per procedure. Procedures without a rule are free.
type Limiter struct {
	rules map[string]Rule
	clock clock.Clock

	mu      sync.Mutex
	buckets map[key]*bucket
	calls   int
}

// New returns a Limiter enforcing rules, keyed by full procedure name such
// as "/kuepreorder.orders.v1.CheckoutService/QuoteOrder".
func New(clk clock.Clock, rules map[string]Rule) *Limiter {
	return &Limiter{rules: rules, clock: clk, buckets: map[key]*bucket{}}
}

// Allow reports whether client may call procedure now, spending a token if so.
func (l *Limiter) Allow(procedure, client string) bool {
	rule, ok := l.rules[procedure]
	if !ok {
		return true
	}
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls++; l.calls%sweepEvery == 0 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > b.idle {
				delete(l.buckets, k)
			}
		}
	}
	k := key{procedure, client}
	b, ok := l.buckets[k]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(rate.Every(rule.Every), rule.Burst), idle: rule.Every * time.Duration(rule.Burst)}
		l.buckets[k] = b
	}
	b.last = now
	return b.limiter.AllowN(now, 1)
}

// ErrTooManyRequests is returned, as ResourceExhausted, to a client over its limit.
var ErrTooManyRequests = errors.New("too many requests, try again shortly")

// Interceptor rejects unary calls over their limit with ResourceExhausted,
// counting per the address httpserver.ClientIP stored.
func (l *Limiter) Interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if !req.Spec().IsClient && !l.Allow(req.Spec().Procedure, httpserver.ClientIPFrom(ctx)) {
				return nil, connect.NewError(connect.CodeResourceExhausted, ErrTooManyRequests)
			}
			return next(ctx, req)
		}
	})
}

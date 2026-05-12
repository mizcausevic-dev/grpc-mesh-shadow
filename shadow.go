// Package shadow provides a gRPC client wrapper that mirrors traffic
// from a stable primary backend to an under-test candidate backend.
// The primary's response is returned synchronously to the caller; the
// candidate's response is captured asynchronously and compared against
// the primary's, with divergences emitted to a configurable sink.
package shadow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"

	pb "github.com/mizcausevic-dev/grpc-mesh-shadow/proto"
)

// DiffEvent records the outcome of a shadow comparison for a single request.
type DiffEvent struct {
	Request        *pb.QuoteRequest
	PrimaryResp    *pb.QuoteResponse
	CandidateResp  *pb.QuoteResponse
	PrimaryErr     error
	CandidateErr   error
	PrimaryLatency time.Duration
	ShadowLatency  time.Duration
	Divergent      bool
	DivergenceKind string // "primary_error", "candidate_error", "value_diff", or "" if no divergence
}

// DiffSink receives diff events. Implementations MUST be safe for concurrent use.
type DiffSink interface {
	Record(DiffEvent)
}

// MemoryDiffSink stores events in memory. Useful for tests and demos.
type MemoryDiffSink struct {
	mu     sync.Mutex
	events []DiffEvent
}

// Record appends an event.
func (s *MemoryDiffSink) Record(e DiffEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// Events returns a snapshot of all recorded events.
func (s *MemoryDiffSink) Events() []DiffEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DiffEvent, len(s.events))
	copy(out, s.events)
	return out
}

// DivergentCount returns the number of events flagged as divergent.
func (s *MemoryDiffSink) DivergentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.Divergent {
			n++
		}
	}
	return n
}

// Config configures the shadow client.
type Config struct {
	// CandidateTimeout caps how long the candidate has to respond before
	// the shadow path is abandoned. Does not affect the primary path.
	CandidateTimeout time.Duration
	// SyncShadow forces the candidate call onto the primary's goroutine,
	// blocking the primary's response until the candidate also returns.
	// Off by default; on in tests so they can assert event capture
	// without polling.
	SyncShadow bool
	// SamplingRate is the fraction of primary requests that are mirrored
	// to the candidate. Defaults to 1.0 (mirror everything).
	SamplingRate float64
}

// DefaultConfig returns a reasonable production default.
func DefaultConfig() Config {
	return Config{
		CandidateTimeout: 5 * time.Second,
		SyncShadow:       false,
		SamplingRate:     1.0,
	}
}

// Client is a typed shadow wrapper for PricingService.
type Client struct {
	primary   pb.PricingServiceClient
	candidate pb.PricingServiceClient
	sink      DiffSink
	cfg       Config

	mu       sync.Mutex
	counter  uint64
}

// NewClient builds a shadow client. `primary` is the canonical backend
// whose responses are returned to callers; `candidate` is the
// under-test backend whose responses are diffed but never returned.
func NewClient(
	primary pb.PricingServiceClient,
	candidate pb.PricingServiceClient,
	sink DiffSink,
	cfg Config,
) *Client {
	if sink == nil {
		sink = &MemoryDiffSink{}
	}
	if cfg.SamplingRate <= 0 {
		cfg.SamplingRate = 1.0
	}
	if cfg.CandidateTimeout <= 0 {
		cfg.CandidateTimeout = 5 * time.Second
	}
	return &Client{
		primary:   primary,
		candidate: candidate,
		sink:      sink,
		cfg:       cfg,
	}
}

// NewClientFromConns builds a shadow client from raw gRPC connections.
func NewClientFromConns(
	primary, candidate *grpc.ClientConn,
	sink DiffSink,
	cfg Config,
) *Client {
	return NewClient(
		pb.NewPricingServiceClient(primary),
		pb.NewPricingServiceClient(candidate),
		sink,
		cfg,
	)
}

// Quote returns the primary backend's response. If sampled, the
// candidate is called in parallel and the comparison is recorded.
func (c *Client) Quote(ctx context.Context, req *pb.QuoteRequest, opts ...grpc.CallOption) (*pb.QuoteResponse, error) {
	primaryStart := time.Now()
	primaryResp, primaryErr := c.primary.Quote(ctx, req, opts...)
	primaryLatency := time.Since(primaryStart)

	if !c.sampleThisRequest() {
		return primaryResp, primaryErr
	}

	shadowFn := func() {
		shadowCtx, cancel := context.WithTimeout(context.Background(), c.cfg.CandidateTimeout)
		defer cancel()
		shadowStart := time.Now()
		candidateResp, candidateErr := c.candidate.Quote(shadowCtx, req)
		event := buildDiffEvent(req, primaryResp, candidateResp, primaryErr, candidateErr, primaryLatency, time.Since(shadowStart))
		c.sink.Record(event)
	}

	if c.cfg.SyncShadow {
		shadowFn()
	} else {
		go shadowFn()
	}

	return primaryResp, primaryErr
}

func (c *Client) sampleThisRequest() bool {
	if c.cfg.SamplingRate >= 1.0 {
		return true
	}
	c.mu.Lock()
	c.counter++
	idx := c.counter
	c.mu.Unlock()
	// deterministic threshold sampling: mirror if (idx * rate) crosses a new integer.
	return float64(idx)*c.cfg.SamplingRate-float64(uint64(float64(idx-1)*c.cfg.SamplingRate)) >= 1.0
}

func buildDiffEvent(
	req *pb.QuoteRequest,
	primaryResp, candidateResp *pb.QuoteResponse,
	primaryErr, candidateErr error,
	primaryLatency, shadowLatency time.Duration,
) DiffEvent {
	event := DiffEvent{
		Request:        req,
		PrimaryResp:    primaryResp,
		CandidateResp:  candidateResp,
		PrimaryErr:     primaryErr,
		CandidateErr:   candidateErr,
		PrimaryLatency: primaryLatency,
		ShadowLatency:  shadowLatency,
	}
	switch {
	case primaryErr != nil && candidateErr == nil:
		event.Divergent = true
		event.DivergenceKind = "primary_error"
	case primaryErr == nil && candidateErr != nil:
		event.Divergent = true
		event.DivergenceKind = "candidate_error"
	case primaryErr == nil && candidateErr == nil:
		if !quotesEqual(primaryResp, candidateResp) {
			event.Divergent = true
			event.DivergenceKind = "value_diff"
		}
	}
	return event
}

func quotesEqual(a, b *pb.QuoteResponse) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.GetSku() == b.GetSku() &&
		a.GetUnitPriceUsd() == b.GetUnitPriceUsd() &&
		a.GetTotalPriceUsd() == b.GetTotalPriceUsd()
}

// String renders an event suitable for log output.
func (e DiffEvent) String() string {
	if !e.Divergent {
		return fmt.Sprintf(
			"shadow ok sku=%s tenant=%s primary=%.4f shadow=%.4f primary_latency=%s shadow_latency=%s",
			e.Request.GetSku(), e.Request.GetTenantId(),
			e.PrimaryResp.GetTotalPriceUsd(), e.CandidateResp.GetTotalPriceUsd(),
			e.PrimaryLatency, e.ShadowLatency,
		)
	}
	return fmt.Sprintf(
		"shadow DIVERGENT kind=%s sku=%s tenant=%s primary=%v candidate=%v",
		e.DivergenceKind, e.Request.GetSku(), e.Request.GetTenantId(),
		e.PrimaryResp, e.CandidateResp,
	)
}

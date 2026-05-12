package shadow

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/mizcausevic-dev/grpc-mesh-shadow/proto"
)

// stubBackend implements PricingService with configurable responses.
type stubBackend struct {
	pb.UnimplementedPricingServiceServer
	algorithm string
	multiplier float64
	failNext   bool
}

func (s *stubBackend) Quote(ctx context.Context, req *pb.QuoteRequest) (*pb.QuoteResponse, error) {
	if s.failNext {
		s.failNext = false
		return nil, context.DeadlineExceeded
	}
	unit := 10.0 * s.multiplier
	return &pb.QuoteResponse{
		Sku:           req.GetSku(),
		UnitPriceUsd:  unit,
		TotalPriceUsd: unit * float64(req.GetQuantity()),
		AlgorithmId:   s.algorithm,
	}, nil
}

// dial spins up an in-process gRPC server for the given backend and
// returns a connected client.
func dialBackend(t *testing.T, backend *stubBackend) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterPricingServiceServer(srv, backend)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func TestShadow_NoDivergence_RecordsMatchingEvent(t *testing.T) {
	primary := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})
	candidate := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})

	sink := &MemoryDiffSink{}
	cfg := DefaultConfig()
	cfg.SyncShadow = true
	client := NewClientFromConns(primary, candidate, sink, cfg)

	resp, err := client.Quote(context.Background(), &pb.QuoteRequest{
		Sku: "WIDGET-9", TenantId: "tnt_acme", Quantity: 3,
	})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if resp.GetTotalPriceUsd() != 30.0 {
		t.Errorf("total = %v, want 30.0", resp.GetTotalPriceUsd())
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("event count = %d, want 1", got)
	}
	if sink.DivergentCount() != 0 {
		t.Errorf("DivergentCount = %d, want 0", sink.DivergentCount())
	}
}

func TestShadow_ValueDiff_DetectedAndRecorded(t *testing.T) {
	primary := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})
	candidate := dialBackend(t, &stubBackend{algorithm: "v2-experimental", multiplier: 1.05})

	sink := &MemoryDiffSink{}
	cfg := DefaultConfig()
	cfg.SyncShadow = true
	client := NewClientFromConns(primary, candidate, sink, cfg)

	primaryResp, err := client.Quote(context.Background(), &pb.QuoteRequest{
		Sku: "WIDGET-9", TenantId: "tnt_acme", Quantity: 10,
	})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if primaryResp.GetTotalPriceUsd() != 100.0 {
		t.Errorf("primary total = %v, want 100.0", primaryResp.GetTotalPriceUsd())
	}
	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if !events[0].Divergent {
		t.Error("event should be flagged divergent")
	}
	if events[0].DivergenceKind != "value_diff" {
		t.Errorf("DivergenceKind = %q, want value_diff", events[0].DivergenceKind)
	}
}

func TestShadow_CandidateError_FlaggedAsDivergent(t *testing.T) {
	primary := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})
	candidate := dialBackend(t, &stubBackend{algorithm: "v2", multiplier: 1.0, failNext: true})

	sink := &MemoryDiffSink{}
	cfg := DefaultConfig()
	cfg.SyncShadow = true
	client := NewClientFromConns(primary, candidate, sink, cfg)

	_, err := client.Quote(context.Background(), &pb.QuoteRequest{Sku: "X", Quantity: 1})
	if err != nil {
		t.Fatalf("Quote should succeed on primary path: %v", err)
	}
	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].DivergenceKind != "candidate_error" {
		t.Errorf("DivergenceKind = %q, want candidate_error", events[0].DivergenceKind)
	}
}

func TestShadow_Sampling_PartialFanout(t *testing.T) {
	primary := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})
	candidate := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})

	sink := &MemoryDiffSink{}
	cfg := Config{SyncShadow: true, SamplingRate: 0.5, CandidateTimeout: time.Second}
	client := NewClientFromConns(primary, candidate, sink, cfg)

	const total = 20
	for i := 0; i < total; i++ {
		_, _ = client.Quote(context.Background(), &pb.QuoteRequest{Sku: "X", Quantity: 1})
	}
	mirrored := len(sink.Events())
	if mirrored < 7 || mirrored > 13 {
		t.Errorf("with sampling 0.5 over %d requests, mirrored = %d (expected ~10)", total, mirrored)
	}
}

func TestShadow_AsyncMode_FireAndForget(t *testing.T) {
	primary := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})
	candidate := dialBackend(t, &stubBackend{algorithm: "v1", multiplier: 1.0})

	sink := &MemoryDiffSink{}
	cfg := DefaultConfig() // SyncShadow is false
	client := NewClientFromConns(primary, candidate, sink, cfg)

	_, err := client.Quote(context.Background(), &pb.QuoteRequest{Sku: "X", Quantity: 1})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}

	// Wait briefly for async shadow goroutine.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && len(sink.Events()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(sink.Events()) != 1 {
		t.Errorf("expected 1 event after async fan-out, got %d", len(sink.Events()))
	}
}

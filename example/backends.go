// Package example provides two reference PricingService implementations
// usable as primary and candidate backends in demos and tests.
package example

import (
	"context"

	pb "github.com/mizcausevic-dev/grpc-mesh-shadow/proto"
)

// PrimaryBackend returns deterministic v1 pricing.
type PrimaryBackend struct {
	pb.UnimplementedPricingServiceServer
}

// Quote returns a deterministic unit price of $10 per quantity.
func (b *PrimaryBackend) Quote(_ context.Context, req *pb.QuoteRequest) (*pb.QuoteResponse, error) {
	unit := 10.0
	return &pb.QuoteResponse{
		Sku:           req.GetSku(),
		UnitPriceUsd:  unit,
		TotalPriceUsd: unit * float64(req.GetQuantity()),
		AlgorithmId:   "primary-v1",
	}, nil
}

// CandidateBackend returns experimental v2 pricing that is 5% higher
// — by design, to demonstrate divergence detection.
type CandidateBackend struct {
	pb.UnimplementedPricingServiceServer
}

// Quote returns a 5%-uplift price tagged with the experimental algorithm.
func (b *CandidateBackend) Quote(_ context.Context, req *pb.QuoteRequest) (*pb.QuoteResponse, error) {
	unit := 10.5
	return &pb.QuoteResponse{
		Sku:           req.GetSku(),
		UnitPriceUsd:  unit,
		TotalPriceUsd: unit * float64(req.GetQuantity()),
		AlgorithmId:   "candidate-v2-experimental",
	}, nil
}

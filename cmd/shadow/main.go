// Command shadow runs a demo of the shadow client against two
// in-process PricingService backends, logging divergences to stdout.
package main

import (
	"context"
	"fmt"
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/mizcausevic-dev/grpc-mesh-shadow"
	"github.com/mizcausevic-dev/grpc-mesh-shadow/example"
	pb "github.com/mizcausevic-dev/grpc-mesh-shadow/proto"
)

func main() {
	primaryConn := serveAndDial("primary", &example.PrimaryBackend{})
	candidateConn := serveAndDial("candidate", &example.CandidateBackend{})

	sink := &shadow.MemoryDiffSink{}
	cfg := shadow.DefaultConfig()
	cfg.SyncShadow = true // for deterministic demo output
	client := shadow.NewClientFromConns(primaryConn, candidateConn, sink, cfg)

	fmt.Println("shadow demo: 5 quotes through primary, mirrored to candidate")
	fmt.Println("--------------------------------------------------------------")
	for i := 1; i <= 5; i++ {
		resp, err := client.Quote(context.Background(), &pb.QuoteRequest{
			Sku: "WIDGET-9", TenantId: "tnt_demo", Quantity: int32(i),
		})
		if err != nil {
			log.Fatalf("quote: %v", err)
		}
		fmt.Printf("client received: sku=%s qty=%d total=$%.2f algo=%s\n",
			resp.GetSku(), i, resp.GetTotalPriceUsd(), resp.GetAlgorithmId())
	}

	fmt.Println("\nshadow events:")
	for _, e := range sink.Events() {
		fmt.Printf("  %s\n", e)
	}
	fmt.Printf("\ndivergent: %d/%d\n", sink.DivergentCount(), len(sink.Events()))
}

func serveAndDial(name string, impl pb.PricingServiceServer) *grpc.ClientConn {
	listener := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterPricingServiceServer(srv, impl)
	go func() {
		if err := srv.Serve(listener); err != nil {
			log.Fatalf("%s serve: %v", name, err)
		}
	}()
	conn, err := grpc.NewClient(
		"passthrough:///"+name,
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("%s dial: %v", name, err)
	}
	return conn
}

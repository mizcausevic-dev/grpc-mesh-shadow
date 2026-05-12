# grpc-mesh-shadow

A typed gRPC **shadow traffic** client — primary backend's responses go to your users, candidate backend's responses get diffed in the background. Built for the boring-but-critical SRE problem of testing a new service version against production traffic *without* exposing users to its bugs.

## What it does

Wrap any `pb.PricingServiceClient` pair with `shadow.NewClient(primary, candidate, sink, cfg)`. Every call goes to the **primary**; the response is returned to your code immediately. In parallel (or synchronously for tests), the **candidate** is called with the same request. The two responses are diffed; mismatches, errors, and latency profiles are emitted to a configurable `DiffSink`.

Use it when:
- You're rewriting a hot-path service and need confidence the new version computes the same results
- You're migrating between providers (e.g. two pricing engines) and want continuous comparison
- You want production traffic in your eval harness without putting the candidate in the user path

## Quickstart

```bash
go install github.com/mizcausevic-dev/grpc-mesh-shadow/cmd/shadow@latest
shadow   # runs the in-process demo
```

The demo output:

```
shadow demo: 5 quotes through primary, mirrored to candidate
--------------------------------------------------------------
client received: sku=WIDGET-9 qty=1 total=$10.00 algo=primary-v1
client received: sku=WIDGET-9 qty=2 total=$20.00 algo=primary-v1
client received: sku=WIDGET-9 qty=3 total=$30.00 algo=primary-v1
client received: sku=WIDGET-9 qty=4 total=$40.00 algo=primary-v1
client received: sku=WIDGET-9 qty=5 total=$50.00 algo=primary-v1

shadow events:
  shadow DIVERGENT kind=value_diff sku=WIDGET-9 tenant=tnt_demo ...
  (5 total)

divergent: 5/5
```

## Library usage

```go
import (
    "github.com/mizcausevic-dev/grpc-mesh-shadow"
    pb "github.com/mizcausevic-dev/grpc-mesh-shadow/proto"
)

primary := pb.NewPricingServiceClient(primaryConn)
candidate := pb.NewPricingServiceClient(candidateConn)
sink := &shadow.MemoryDiffSink{}   // or write a custom DiffSink

cfg := shadow.DefaultConfig()
cfg.SamplingRate = 0.1   // mirror 10% of traffic
client := shadow.NewClient(primary, candidate, sink, cfg)

resp, err := client.Quote(ctx, req)  // returns primary; candidate runs in background
```

## Config

| Field | Default | What |
|---|---|---|
| `CandidateTimeout` | `5s` | How long the candidate has to respond. Has no effect on primary latency. |
| `SyncShadow` | `false` | Block on candidate before returning primary. Off in production; on for deterministic tests. |
| `SamplingRate` | `1.0` | Fraction of primary requests mirrored to candidate. |

## DiffEvent

```go
type DiffEvent struct {
    Request        *pb.QuoteRequest
    PrimaryResp    *pb.QuoteResponse
    CandidateResp  *pb.QuoteResponse
    PrimaryErr     error
    CandidateErr   error
    PrimaryLatency time.Duration
    ShadowLatency  time.Duration
    Divergent      bool
    DivergenceKind string   // "primary_error" | "candidate_error" | "value_diff" | ""
}
```

Implement your own `DiffSink` to forward to your observability stack (Datadog, Prometheus, etc.).

## Compatibility

- Go `1.22+`
- google.golang.org/grpc `v1.66+`
- google.golang.org/protobuf `v1.36+`

The proto file is at [proto/pricing.proto](proto/pricing.proto). Generated `.pb.go` files are checked in, so you do not need `protoc` to build this project — only to regenerate after changing the proto.

## Development

```bash
go vet ./...
go test -race -v ./...
go build ./...
```

Tests use `bufconn` to stand up the primary and candidate gRPC servers in-process. No network required.

## License

AGPL-3.0.

---

**Connect:** [LinkedIn](https://www.linkedin.com/in/mirzacausevic/) · [Kinetic Gain](https://kineticgain.com) · [Medium](https://medium.com/@mizcausevic/) · [Skills](https://mizcausevic.com/skills/)

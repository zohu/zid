# zid

[![Go Reference](https://pkg.go.dev/badge/github.com/zohu/zid.svg)](https://pkg.go.dev/github.com/zohu/zid)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Short, ordered, fast — with autonomous worker-ID leases.**

`zid` is a high-performance, concurrency-safe `int64` ID generator for Go. It
produces compact, millisecond-sortable IDs through a zero-allocation integer hot
path, while optional Redis and Kubernetes managers automate worker allocation,
lease renewal, fencing, and recovery.

It is designed for databases and distributed services that want the storage and
index efficiency of numeric Snowflake IDs together with a complete, tested
worker lifecycle.

**English** · [简体中文](README_ZH.md)

## Why zid

- **Short** — a positive `int64` occupies 8 bytes in a database, at most 19
  decimal digits or 11 base-62 characters.
- **Ordered** — numeric order follows millisecond time, improving index locality
  and making creation time directly extractable.
- **Fast** — the integer hot path is zero-allocation; local sharding scales
  concurrent generation to roughly 28–30 million IDs/s in the included benchmark.
- **Autonomous distributed workers** — Redis and Kubernetes managers handle
  worker acquisition, renewal, fencing, and automatic replacement after lease
  loss.
- **Uniqueness first** — when a worker can no longer be proven safe, generation
  pauses and resumes with a different worker instead of emitting a risky ID.
- **High capacity** — the default layout provides 262,144 IDs per millisecond per
  worker, with configurable worker, shard, and sequence bits.
- **Clock rollback aware** — rollback consumes the remaining safe sequence at the
  last observed millisecond and never manufactures capacity by repeatedly moving
  logical time forward.
- **Simple application API** — configure once, then call `zid.NextID()` anywhere;
  use `zid.NextIDContext` when a request needs cancellation.
- **Focused dependencies** — the core module has no third-party runtime
  dependencies; Redis and Kubernetes integrations are independent Go modules.

## Quick start

### Install

```shell
go get github.com/zohu/zid@latest
```

### Package-level generator

For one process, the package default is the shortest path:

```go
package main

import (
    "fmt"

    "github.com/zohu/zid"
)

func main() {
    id := zid.NextID()

    fmt.Println(id)
    fmt.Println(zid.ExtractTime(id))
    fmt.Println(zid.ExtractWorkerID(id))
}
```

The initial package default uses worker ID `0` for a single process. For a
multi-process deployment, install a managed default before the first
package-level generation or extraction call.

### Independent generator

Use an explicit generator for dependency injection, a custom layout, or an
application-assigned worker ID:

```go
generator, err := zid.New(zid.Options{
    WorkerID:     3,
    WorkerIDBits: 8,
    ShardBits:    4,
    SequenceBits: 10,
})
if err != nil {
    return err
}
defer generator.Close()

id := generator.NextID()
createdAt := generator.ExtractTime(id)
workerID := generator.ExtractWorkerID(id)
shardID := generator.ExtractShardID(id)
```

This layout supports 256 workers, 16 local shards per worker, and 1,024 IDs per
millisecond per shard.

### Context, strings, and parsing

```go
id, err := zid.NextIDContext(requestContext)
if err != nil {
    // context cancellation/deadline, or zid.ErrClosed
    return err
}

decimal := zid.NextString()
hex := zid.NextHex()
base36 := zid.NextBase36()
base62 := zid.NextBase62()

parsed, err := zid.ParseBase62(base62)
if err != nil {
    return err
}
generatedAt := zid.ExtractTime(parsed)
```

`ParseHex`, `ParseBase36`, and `ParseBase62` reject empty, malformed, negative,
or overflowing values. String creation allocates; the integer `NextID` path does
not. Every service that extracts fields from an ID must use the same epoch and
bit layout that generated it.

## How it works

### ID layout

`zid` stores a non-negative ID in a signed `int64`:

```text
  63 usable bits
┌──────────────────────┬────────────┬───────────┬────────────┐
│ timestamp (>=41 bits)│ worker (w) │ shard (s) │ sequence(q)│
└──────────────────────┴────────────┴───────────┴────────────┘
                         w + s + q <= 22 bits
```

The timestamp is the number of real milliseconds since the protocol epoch. The
default protocol is:

| Property | Default |
|---|---:|
| Epoch | `2026-08-01 00:00:00 UTC` |
| Unix epoch value | `1785542400000` ms |
| Worker bits | 4 |
| Local shard bits | 0 |
| Sequence bits | 18 |
| Workers | 16 |
| IDs per millisecond per worker | 262,144 |
| Minimum lifetime | about 69.7 years |

IDs sort numerically by millisecond timestamp. Generation order inside the same
millisecond is not a total order when multiple workers or local shards are used.

### Generation path

1. Select a local shard. With one shard this is the only shard; with multiple
   shards, selection spreads lock contention.
2. Verify that the generator is open and, in managed mode, that its lease is
   still inside the local safety window.
3. Read the wall-clock millisecond. A new millisecond resets the shard sequence.
4. During clock rollback, hold the last real millisecond already observed and
   consume its remaining sequence; do not keep incrementing logical time.
5. If the selected shard is full, try every other shard before waiting.
6. If all shards are full, one goroutine waits for the real clock to advance;
   other callers wait on a shared notification instead of creating polling
   timers of their own.

The result is uniqueness without inventing future timestamps. The tradeoff is
intentional backpressure when real-time capacity has actually been exhausted.

### Uniqueness scopes

| Construction | Guarantee scope | Caller responsibility |
|---|---|---|
| Package default | One process lifetime | Do not use the initial worker `0` default in multiple processes |
| `zid.New` | One generator lifetime | Assign distinct worker IDs to concurrent generators/processes |
| `zid.NewManaged` | Distributed, while the coordinator assumptions hold | Operate Redis/Kubernetes safely and keep a free worker ID available |

Reconstructing a process-local generator with the same worker ID can repeat the
last millisecond's sequence. Use managed mode when uniqueness must survive
process restarts without application-managed fencing.

## Distributed worker IDs

Redis and Kubernetes managers are separate modules, so applications only pull
the coordinator they use:

```shell
go get github.com/zohu/zid/zidredis@latest
go get github.com/zohu/zid/zidk8s@latest
```

### Redis

The one-step startup API creates the manager and installs it as the package
default:

```go
package main

import (
    "context"

    "github.com/redis/go-redis/v9"
    "github.com/zohu/zid"
    "github.com/zohu/zid/zidredis"
)

func configureIDs(ctx context.Context, client redis.UniversalClient) error {
    if err := zidredis.ConfigureDefault(
        ctx,
        client,
        zidredis.Options{},
        zid.Options{WorkerIDBits: 10, SequenceBits: 12},
    ); err != nil {
        return err
    }

    // Call zid.CloseDefault during application shutdown.
    return nil
}
```

Call `ConfigureDefault` exactly once during startup, before any package-level
generation or extraction helper. A later attempt returns
`zid.ErrDefaultAlreadyUsed`. `zidredis.New` remains side-effect free when an
explicit `zid.NewManaged` generator is preferable.

```go
manager, err := zidredis.New(client, zidredis.Options{})
if err != nil {
    return err
}
generator, err := zid.NewManaged(ctx, manager, zid.Options{})
if err != nil {
    return err
}
defer generator.Close()
```

Redis uses `SET NX PX` with a random owner token. Renewal executes a Lua
compare-and-`PEXPIRE`, so an old owner cannot renew a newer owner's key. Closing
a lease does not delete the key immediately; the remaining TTL acts as a worker
ID reuse quarantine.

### Kubernetes

```go
if err := zidk8s.ConfigureDefault(
    ctx,
    zidk8s.Options{},
    zid.Options{WorkerIDBits: 10, SequenceBits: 12},
); err != nil {
    return err
}
defer zid.CloseDefault()

id := zid.NextID()
```

Defaults read `NAMESPACE` and `POD_UID` from the environment and use in-cluster
configuration. Both can be set explicitly in `zidk8s.Options`.

Minimum namespaced RBAC:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: zid-lease-manager
rules:
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update"]
```

Each Kubernetes holder identity includes a random instance token. Claims and
renewals use `resourceVersion` compare/update semantics. Non-`NotFound` API
errors are never treated as an absent Lease, and closed Leases remain until
expiry as a reuse quarantine.

### Lease lifecycle and recovery

Both managers use these defaults:

| Setting | Default |
|---|---:|
| Lease TTL | 30 s |
| Renewal interval | 10 s |
| Safety margin | 10 s |
| Operation timeout | 3 s |

The local safety deadline is computed conservatively from the start of the
coordinator request. Renewal and recovery behave as follows:

| Generator state | Can emit IDs? | Behavior |
|---|---:|---|
| `GeneratorHealthy` | Yes | Lease is valid and renewals are succeeding |
| `GeneratorDegraded` | Yes | A transient renewal failed, but the local safety deadline has not passed |
| `GeneratorRecovering` | No | The old lease is fenced; a different worker ID is being acquired |
| `GeneratorClosed` | No | The generator was explicitly closed |

Use `State` or package-level `DefaultState` for readiness and metrics. `Healthy`
means “safe to emit an ID now”, so it remains true while the state is degraded;
use the full state when coordinator health must affect readiness.

On permanent loss, the generator:

1. Stops emitting IDs before the lease can be considered unsafe.
2. Permanently excludes that worker ID for the generator's remaining lifetime.
3. Retries acquisition with exponential backoff from 100 ms up to 5 s.
4. Installs a different worker ID atomically across all local shards.
5. Wakes blocked callers after the new lease is safe.

Direct `NextID`, `NextString`, `NextHex`, `NextBase36`, and `NextBase62` calls
wait during recovery rather than return a possibly duplicated ID. Use
`NextIDContext` where a request must have a deadline.

If the coordinator is temporarily unavailable or no eligible worker ID is free,
generation resumes automatically as soon as a safe lease becomes available.

## Configuration

### Generator options

| Field | Zero/default behavior | Valid range or note |
|---|---|---|
| `BaseTime` | `2026-08-01 UTC` | Must be positive and not in the future |
| `WorkerID` | `0` | Must fit `WorkerIDBits`; assigned by managed mode |
| `WorkerIDBits` | `4` | 0–19 bits |
| `ShardBits` | `0` | 0–16 bits |
| `SequenceBits` | `18` | 3–22 bits |
| `DisableWorkerID` | `false` | Cannot be combined with `WorkerID` or `WorkerIDBits` |

`WorkerIDBits + ShardBits + SequenceBits` must be at most 22. Reducing the low
bit count increases timestamp lifetime; reallocating low bits trades worker
count, local parallelism, and per-shard capacity.

For a generator that spends no bits on worker identity:

```go
generator, err := zid.New(zid.Options{
    DisableWorkerID: true,
    ShardBits:       4,
    SequenceBits:    18,
})
```

## Safety and availability behavior

`zid` preserves uniqueness with explicit backpressure whenever generation cannot
continue safely:

| Condition | Direct `Next...` methods | `NextIDContext` |
|---|---|---|
| Capacity remains | Return immediately | Return immediately |
| All shards full in current real millisecond | Wait for the real next millisecond | Wait or return the context error |
| Managed lease is recovering | Wait for a different safe worker ID | Wait or return the context error |
| Generator is closed | Panic with `zid.ErrClosed` | Return `zid.ErrClosed` |

`Close` is idempotent. A direct call after `Close` is treated as a lifecycle
programming error and panics.

### Distributed assumptions

Managed uniqueness requires all of the following:

- Redis or Kubernetes preserves acknowledged lease writes and provides reliable
  compare/update semantics. A Redis failover that loses an acknowledged write
  can break exclusivity.
- Pairwise node-clock offset and worst-case coordinator latency remain below the
  configured `SafetyMargin`.
- All participants sharing a Redis prefix or Kubernetes Lease prefix use the
  same TTL and safety settings.
- The configured worker-ID space has a free eligible lease. A recovering
  generator never reuses a worker ID it previously lost, so repeated permanent
  losses can exhaust its candidate set.
- All consumers agree on the epoch and bit layout.
- The signed `int64` timestamp range has not been exhausted.

During a network partition or coordinator outage, `zid` prioritizes uniqueness
and resumes generation when it can prove that a safe lease is available.

## Comparison with other Go ID libraries

These libraries solve different problems. The table compares protocol and
operational properties, not benchmark results from different machines.

| Library | ID shape | Time ordering | Uniqueness / coordination model | Best fit |
|---|---|---|---|---|
| **zid** | 63-bit positive `int64`; decimal, hex, base36, base62 | Millisecond, numeric | Exact sequence within an exclusive worker; built-in optional Redis/Kubernetes leases | Compact database keys with explicit distributed fencing |
| [bwmarrin/snowflake](https://github.com/bwmarrin/snowflake) | 63-bit `int64`; default 41 time + 10 node + 12 sequence bits | Millisecond, numeric | Application must keep node numbers unique | Simple classic Snowflake with externally managed nodes |
| [Sonyflake](https://github.com/sony/sonyflake) | 63-bit integer; default 39 time + 8 sequence + 16 machine bits | 10 ms by default | Machine-ID callback or host-derived default; optional validation | Long lifetime or a large machine-ID space |
| [ULID](https://github.com/oklog/ulid) | 128 bits; canonical 26-char base32 | Millisecond, lexicographic; optional monotonic entropy | Random entropy, coordination-free and probabilistic | Portable, standard-shaped sortable strings |
| [KSUID](https://github.com/segmentio/ksuid) | 160 bits; canonical 27-char base62 | Second, lexicographic | 128-bit random payload, coordination-free and probabilistic | Portable strings with strong collision resistance |

`zid` is usually a better fit when:

- the database and APIs benefit from an 8-byte numeric key;
- worker ownership must be explicit and observable rather than inferred from
  host metadata;
- lease renewal, fencing, and automatic worker replacement should have one
  tested implementation;
- deterministic per-worker capacity is preferred over probabilistic collision
  resistance;
- high local concurrency must not serialize on one generator mutex.

Selection note: use a cryptographically random token when unpredictability is a
security requirement. ULID, KSUID, or
[UUIDv7](https://www.rfc-editor.org/rfc/rfc9562.html#name-uuid-version-7) are
natural choices when canonical cross-language strings or coordination-free
generation matter more than compact numeric storage. Strict global total order
requires a distributed sequencing service.

## Performance

Benchmarks were run on Apple M1 Max with Go 1.26.2. They are measurements, not a
cross-library claim:

| Benchmark | Typical latency | Approximate throughput | Allocations |
|---|---:|---:|---:|
| `BenchmarkGeneratorSingle` | 55–59 ns/op | 17–18 M IDs/s | 0 B/op, 0 allocs/op |
| `BenchmarkPackageNextID` | 55–57 ns/op | 17–18 M IDs/s | 0 B/op, 0 allocs/op |
| `BenchmarkGeneratorShardedParallel` | 33–36 ns/op | 28–30 M IDs/s | 0 B/op, 0 allocs/op |

The measured CPU throughput and the protocol capacity are different limits. The
default format has space for 262,144 IDs/ms/worker; the benchmark above measures
how fast this implementation generated IDs on one machine.

Reproduce locally:

```shell
go test -run '^$' \
  -bench 'Benchmark(GeneratorSingle|PackageNextID|GeneratorShardedParallel)$' \
  -benchmem -count=8
```

## Development

Run the full workspace verification:

```shell
go test -race ./...
go vet ./...

GOWORK=off go -C zidredis test -race ./...
GOWORK=off go -C zidredis vet ./...

GOWORK=off go -C zidk8s test -race ./...
GOWORK=off go -C zidk8s vet ./...
```

Current statement coverage is approximately 85.7% for the core, 86.6% for the
Redis module, and 81.9% for the Kubernetes module.

## Contributing

Contributions should include focused tests for protocol, clock, concurrency, or
lease-lifecycle changes. Please open an issue before changing the ID protocol or
distributed safety model.

## Maintainer release

All three modules are tagged from the same commit. After the two submodules
require the target root version and the working tree is committed and clean:

```shell
mise release v1.0.0
```

The task independently runs `go mod tidy -diff`, `go vet`, and race-enabled
tests for all modules, creates `v1.0.0`, `zidredis/v1.0.0`, and
`zidk8s/v1.0.0`, then atomically pushes the branch and tags.

## License

[MIT](LICENSE)

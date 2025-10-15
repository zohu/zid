## 🌟 Enhanced Snowflake ID Generator

> A high-performance, zero-allocation, zero-dependency, low-latency, and customizable Snowflake ID generation library\
> **Deeply optimized clock drift handling**\
> Supports **fully customizable bit layout**\
> Balances flexibility and high availability.

[中文文档](https://github.com/zohu/zid/blob/main/README_ZH.md)

### 🔑 Use this library if you have any of the following requirements:

* **Flexible bit allocation for shorter globally unique IDs**
* **Automatic clock drift protection with minimal impact on uniqueness**
* **Overload tolerance under instantaneous high concurrency**
* **Extremely high single-node concurrency**
* **Massively scaled nodes or IoT scenarios**
* **Automatic WorkerId assignment**

***

### 🛠 Usage Guide

#### 1. Install Dependency

```shell
go get github.com/zohu/zid
```

#### 2. Configuration Reference

| Field Name             | Type     | Description                                                                                                         |
|------------------------|----------|---------------------------------------------------------------------------------------------------------------------|
| `BaseTime`             | `int64`  | Base timestamp in milliseconds; must not exceed current system time. Default: 2025-10-01                            |
| `WorkerId`             | `int64`  | Machine identifier. Maximum value: `(2^WorkerIdBitLength - 1)`                                                     |
| `WorkerIdBitLength`    | `byte`   | Bit length for WorkerId. Default: `4`. Range: `[1~19, 'f']` (Requirement: `SeqBitLength + WorkerIdBitLength ≤ 22`). Setting to `'f'` disables WorkerId. |
| `SeqBitLength`         | `byte`   | Bit length for sequence number. Default: `6`. Range: `[3~21, 22]` (Requirement: `SeqBitLength + WorkerIdBitLength ≤ 22`). Only when `WorkerIdBitLength='f'` can this be set to `22`. |
| `MaxSeqNumber`         | `uint32` | Maximum sequence number (inclusive). Range: `[MinSeqNumber, 2^SeqBitLength - 1]`. Default `0` means use max value `(2^SeqBitLength - 1)`. |
| `MinSeqNumber`         | `uint32` | Minimum sequence number (inclusive). Default: `5`. Range: `[5, MaxSeqNumber]`. The first 5 sequence numbers per millisecond (0–4) are reserved: 0 for manual assignment, 1–4 for clock drift fallback. |
| `TopOverCostCount`     | `uint32` | Maximum allowed drift count (inclusive). Default: `2000`. Recommended range: `500–10000` (for high-concurrency fault tolerance). |
| `ShardedMode`          | `bool`   | High-performance single-node mode. Default: `false`. If enabled, `WorkerId` is ignored, and `WorkerIdBitLength` controls the number of shards. |

#### 3. Basic Usage

```go
// Monolithic services can use directly (WorkerId defaults to 0)
zid.NextInt()           // → 1222633405189
zid.NextString()        // → "1222633405189"
zid.NextHex()           // → "11caaa13805" (hexadecimal)
zid.NextBase36()        // → "flo4d0n9" (Base36, short ID, case-insensitive)
zid.NextBase62()        // → "lwyFau1" (Base62, even shorter ID, case-sensitive)

// Want even shorter IDs? Customize WorkerIdBitLength and SeqBitLength.
// SeqBitLength affects QPS. If unsure, refer to test cases to find the best fit for your scenario.
// Shortest ID is achieved by setting BaseTime as late as possible, WorkerIdBitLength='f', and SeqBitLength=3.

// Parse ID information
zid.ExtractTime(id)           // → time.Time
zid.ExtractWorkerId(id)       // → int64
// Parsing also supports Hex / Base36 / Base62
zid.ExtractTimeHex("...")
zid.ExtractWorkerIdHex("...")

// Override global configuration (e.g., custom WorkerId or performance tuning)
zid.WithOptions(&zid.Options{"..."})
id := idGen.NextId()

// Create a separate instance, isolated from the global generator
idGen := NewDefaultIdGenerator(&zid.Options{"..."})
id := idGen.NextId()

// Create a sharded instance
idGen := NewShardedGenerator(&zid.Options{"..."})
id := idGen.NextId()
```

#### 4. Large-Scale Nodes (Over 1024 nodes; standard Snowflake insufficient—e.g., edge computing or IoT devices. Full runtime required; TinyGo not supported)

```go
// Increase WorkerIdBitLength, e.g., to 19 → supports up to 524,288 nodes
// You must balance WorkerIdBitLength and SeqBitLength yourself
```

#### 5. Ultra-High Single-Node Concurrency (e.g., high-performance logging, event tracing, sensor streams—scenarios requiring reversible timestamp extraction)

```go
// Option 1: Disable WorkerId entirely by setting WorkerIdBitLength='f'
// WorkerId bits are fully reallocated to SeqBitLength (22 bits), ensuring near-monotonic IDs
zid.WithOptions(&Options{
    WorkerIdBitLength: 'f',
    SeqBitLength:      22,
})

// Option 2: Option 1 uses locking, which degrades performance under concurrency.
// For concurrent scenarios, enable sharded mode for lock-free, allocation-free, atomic-free routing
// Uses fastrand for significantly higher concurrency performance; IDs remain roughly monotonic per millisecond
zid.WithOptions(&Options{
    WorkerIdBitLength: 16,
    SeqBitLength:      6,
    ShardedMode:       true,
})
```

#### 4. Automatic WorkerId Assignment (Distributed Scenarios)

✅ **Built-in Redis-based Auto Assignment** (recommended for bare metal or Docker):

```go
zid.WithOptionsAndWorkerManager(
    zid.NewRedisManager(r redis.UniversalClient),
    &zid.Options{},
)
id := zid.NextId()

// You can also specify a key prefix
zid.WithOptionsAndWorkerManager(
    zid.NewRedisManager(r redis.UniversalClient, "zid"),
    &zid.Options{},
)
```

✅ **Built-in Kubernetes Lease + TTL Auto Cleanup** (recommended for Kubernetes):

```go
zid.WithOptionsAndWorkerManager(
    zid.NewKubernetesManager(&zid.KubernetesOptions{
        PodUID          string // Auto-read from POD_UID env var if unset
        Namespace       string // Auto-read from NAMESPACE env var if unset
        LeaseNamePrefix string // Defaults to "snowflake-" if unset
        Config          *rest.Config // Auto-read from InClusterConfig() if unset
    }),
    &zid.Options{},
)
id := zid.NextId()
```

* Auto-inject UID and NAMESPACE (example):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: your-ns
  name: your-name
spec:
  replicas: 5
  selector:
    matchLabels:
      app: your-app
  template:
    metadata:
      labels:
        app: your-app
    spec:
      containers:
      - name: app
        image: your-app:latest
        env:
        - name: POD_UID
          valueFrom:
            fieldRef:
              fieldPath: metadata.uid
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
```

* RBAC Permissions (example):

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  namespace: your-ns
  name: lease-manager
rules:
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "create", "update"]

---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: lease-manager-binding
  namespace: your-ns
subjects:
- kind: ServiceAccount
  name: default
roleRef:
  kind: Role
  name: lease-manager
  apiGroup: rbac.authorization.k8s.io
```

✅ **Custom Manager**:

```go
// Just implement the WorkerIdManager interface
type WorkerIdManager interface {
    Acquire(ctx context.Context, max int64) error // Acquire a WorkerId
    StartRenewal()  // Start auto-renewal
    Stop()          // Stop
    GetWorkerId() int64
}

// Example: Auto-assign based on IP + MAC
type IpMACWorkerIdManager struct {
    workerId int64
}
func NewIPMACWorkerIdManager() WorkerIdManager {
    return &IpMACWorkerIdManager{}
}
func (m *IpMACWorkerIdManager) Acquire(ctx context.Context, max int64) error{
    ip, mac := getPrimaryIPAndMAC()
    key := ip + "|" + mac
    hash := sha256.Sum256([]byte(key))
    m.workerId = int64(binary.BigEndian.Uint64(hash[:8]) % uint64(max+1))
    return nil
}
func (m *IpMACWorkerIdManager) StartRenewal() {}
func (m *IpMACWorkerIdManager) Stop() {}
func (m *IpMACWorkerIdManager) GetWorkerId() int64 {
    return m.workerId
}
```

### Examples

```shell
snowflake_test.go:15: int64  id=1235891879941, len=13, time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
snowflake_test.go:18: hex    id=11fc0e58006,   len=11, time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
snowflake_test.go:21: base36 id=frre45qf,      len=8,  time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
snowflake_test.go:24: base62 id=lL1Wnt6,       len=7,  time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
```

### Performance
```shell
goos: darwin
goarch: arm64
pkg: zid
cpu: Apple M1 Max
BenchmarkZid_Single_4_18-10         	19951174	        58.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Single_6_16-10         	20591342	        58.20 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Single_8_14-10         	20350092	        58.80 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Single_f_22-10         	20612992	        58.58 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Shard_8_14_10g-10      	33330787	        34.05 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Shard_10_12_50g-10     	33920462	        34.15 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Shard_12_10_100g-10    	39730275	        33.52 ns/op	       0 B/op	       0 allocs/op
BenchmarkZid_Shard_16_6_100g-10     	40579326	        26.91 ns/op	       0 B/op	       0 allocs/op
```

### Future
* Current version already meets most use cases. A CAS-based lock-free version shows similar concurrency performance and is pending optimization.

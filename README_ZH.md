## 🌟 增强型雪花 ID 生成器（Snowflake）

> 高性能、零分配、零依赖、低延迟、可定制 的Snowflake雪花ID生成库\
> **深度优化时钟回拨处理**\
> 支持**完全自定义位分配**\
> 兼顾灵活性与高可用性。

### 🔑 如果你有以下诉求之一，可以使用此库
- **灵活位分配，想要更短的全局唯一ID**
- **尽可能不破坏唯一性的时钟回拨自动保护**
- **瞬时高并发的过载容错**
- **仅单机的超高并发**
- **超大规模节点，或物联网**
- **自动分配WorkerId**

***

### 🛠 使用指南

#### 1. 安装依赖

```bash
go get github.com/zohu/zid
```
#### 2. 配置说明
| 字段名                 | 类型       | 说明                                                                                                         |
|---------------------|----------|------------------------------------------------------------------------------------------------------------|
| `BaseTime`          | `int64`  | 基础时间（毫秒单位），不能超过当前系统时间，默认值为2025-10-01                                                                       |
| `WorkerId`          | `int64`  | 机器码，最大值为 (2^WorkerIdBitLength - 1)                                                                         |
| `WorkerIdBitLength` | `byte`   | 机器码位长，默认值为 `4`，取值范围 `[1~19,f]`（要求：`SeqBitLength + WorkerIdBitLength ≤ 22`）配置为'f'则弃用机器码                     |
| `SeqBitLength`      | `byte`   | 序列数位长，默认值为 `6`，取值范围 `[3~21,22]`（要求：`SeqBitLength + WorkerIdBitLength ≤ 22`）仅当WorkerIdBitLength='f'时，可以配置22 |
| `MaxSeqNumber`      | `uint32` | 最大序列数（含），设置范围 `[MinSeqNumber, 2^SeqBitLength - 1]`，默认值 `0` 表示使用最大值 (2^SeqBitLength - 1)                    |
| `MinSeqNumber`      | `uint32` | 最小序列数（含），默认值 `5`，取值范围 `[5, MaxSeqNumber]`；每毫秒的前5个序列号（0–4）为保留位：0 用于手工新值，1–4 用于时间回拨预留                        |
| `TopOverCostCount`  | `uint32` | 最大漂移次数（含），默认 `2000`，推荐范围 `500–10000`（瞬时高并发的容错能力）                                                           |
| `ShardedMode`       | `bool`   | 单机高性能模式，默认 `false`；若开启，`WorkerId` 将被忽略，`WorkerIdBitLength` 用于控制分片数量                                        |

#### 3. 基础使用

```go
// 单体服务可直接使用（WorkerId 默认为 0）
zid.NextInt()           // → 1222633405189
zid.NextString()        // → "1222633405189"
zid.NextHex()           // → "11caaa13805" (16进制)
zid.NextBase36()        // → "flo4d0n9" (36进制，短ID，适用于不区分大小写的场景)
zid.NextBase62()        // → "lwyFau1" (62进制，更短ID，适用于大小写敏感的场景)
// 还想要更短的ID？可以自行设置WorkerIdBitLength和SeqBitLength
// SeqBitLength影响QPS，拿不定主意可以参考测试用例，适配自己的场景
// 最短的情况就是BaseTime尽可能的晚，然后WorkerIdBitLength='f' SeqBitLength=3

// 解析 ID 信息
zid.ExtractTime(id)           // → time.Time
zid.ExtractWorkerId(id)       // → int64
// 支持 Hex / Base36 / Base62 解析
zid.ExtractTimeHex("...")
zid.ExtractWorkerIdHex("...")

// 自定义配置覆盖，比如自定义workId？或平衡性能？
zid.WithOptions(&zid.Options{"..."})
id := idGen.NextId()

// 单独实例，与全局区分开
idGen := NewDefaultIdGenerator(&zid.Options{"..."})
id := idGen.NextId()
// 单独的分片实例
idGen := NewShardedGenerator(&zid.Options{"..."})
id := idGen.NextId()

```

#### 4. 大规模节点（超过1024节点，标准雪花ID无法满足，如边缘计算、物联网设备，需支持完整运行时，不支持TinyGo）
```go
// 调大WorkerIdBitLength，如19，则支持最多524288个节点
// 需要自己平衡WorkerIdBitLength和SeqBitLength
```

#### 5. 单节点超高并发（如高性能日志/事件追踪/传感器流等需要可逆解析时间的场景）
```go
// 方式一：无并发，完全弃用WorkerIdBitLength，配置WorkerIdBitLength='f'
// 此时WorkerId部分完全弃用，22位全给SeqBitLength使用，基本满足递增
zid.WithOptions(&Options{
    WorkerIdBitLength: 'f',
    SeqBitLength:      22,
})

// 方式二：方式一因为有锁，并发时性能会下降，并发场景还可以如下配置，开启分片模式
// 此时利用fastrand使整个路由过程无锁、无原子操作、无内存分配，并发性能提升N倍，ID毫秒级趋势递增
zid.WithOptions(&Options{
    WorkerIdBitLength: 16,
    SeqBitLength:      6,
    ShardedMode:       true,
})
```

#### 4. 自动分配 WorkerId（分布式场景）
✅ **内置 Redis 自动分配**（裸机或docker推荐）：
```shell
go get -u github.com/zohu/zidredis
```
```go
zid.WithOptionsAndWorkerManager(
    zidredis.NewRedisManager(r redis.UniversalClient),
    &zid.Options{},
)
id := zid.NextId()

// 也可以指定前缀
zid.WithOptionsAndWorkerManager(
    zidredis.NewRedisManager(r redis.UniversalClient, "zid"),
    &zid.Options{},
)
```

✅ **内置基于 Kubernetes Lease（租约） + TTL 自动清理**（Kubernetes推荐）：
```shell
go get -u github.com/zohu/zidk8s
```
```go
zid.WithOptionsAndWorkerManager(
    zidk8s.NewKubernetesManager(&zid.KubernetesOptions{
        PodUID          string // 未设置则自动获取环境变量POD_UID
        Namespace       string // 未设置则自动获取环境变量NAMESPACE
        LeaseNamePrefix string // 未设置则默认"snowflake-"
        Config          *rest.Config // 未设置则自动获取InClusterConfig()
    }),
    &zid.Options{},
)
id := zid.NextId()
```
- 自动注入 UID 和 NAMESPACE (示例)
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
- RBAC 权限 (示例)
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

✅ **自定义管理器**：
```go
// 只需要实现 WorkerIdManager 接口
type WorkerIdManager interface {
    Acquire(ctx context.Context, max int64) error // 生成WorkerId
    StartRenewal()  // 自动续期
    Stop()          // 停止
    GetWorkerId() int64
}

// 示例：基于 IP+MAC 的自动分配
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

### 示例
```shell
snowflake_test.go:15: int64  id=1235891879941, len=13, time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
snowflake_test.go:18: hex    id=11fc0e58006,   len=11, time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
snowflake_test.go:21: base36 id=frre45qf,      len=8,  time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
snowflake_test.go:24: base62 id=lL1Wnt6,       len=7,  time=2025-10-15 07:15:25.664 +0800 CST, workerId=0
```

### 性能
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
- 当前版本已经满足大部分场景，基于CAS的无锁版本并发性能相近，待优化。
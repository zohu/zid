# zid

[![Go Reference](https://pkg.go.dev/badge/github.com/zohu/zid.svg)](https://pkg.go.dev/github.com/zohu/zid)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**短、有序、快，并且具备 Worker ID 租约自治能力。**

`zid` 是面向 Go 的高性能、并发安全 `int64` ID 生成器。它通过零分配整数热路径生成
紧凑、毫秒级有序的 ID，并由可选的 Redis/Kubernetes Manager 自动完成 Worker 分配、
租约续期、fencing 和失租恢复。

它面向需要高效数字主键的数据库与分布式服务，在保留 Snowflake 存储和索引优势的同时，
提供一套完整、经过测试的 Worker 生命周期。

[English](README.md) · **简体中文**

## 为什么选择 zid

- **短**：正 `int64` 在数据库中只占 8 字节，十进制最多 19 位，Base62 最多 11 个字符。
- **有序**：按数值具有毫秒级时间顺序，有利于数据库索引局部性，并可直接提取生成时间。
- **快**：整数热路径零分配；内置本地分片在项目 benchmark 中可将并发生成提升至约
  2800–3000 万 ID/s。
- **分布式 Worker 自治**：Redis/Kubernetes Manager 自动负责 Worker 获取、续租、
  fencing，以及失租后的自动换号。
- **唯一性优先**：无法继续证明 Worker 安全时暂停发号，获得另一个 Worker 后自动恢复，
  不会冒险返回可能重复的 ID。
- **高容量**：默认每个 Worker 每毫秒可容纳 262,144 个 ID，并可重新分配 Worker、
  分片与序列位。
- **感知时钟回拨**：回拨时消耗最后一个已观察毫秒的剩余安全序列，不通过持续增加逻辑
  毫秒来人为扩充容量。
- **应用接入简单**：初始化一次后即可在任意位置调用 `zid.NextID()`；请求需要取消时使用
  `zid.NextIDContext`。
- **依赖面可控**：核心模块没有第三方运行时依赖，Redis/Kubernetes 集成分别位于独立
  Go module 中。

## 快速开始

### 安装

```shell
go get github.com/zohu/zid@latest
```

### 包级默认 Generator

单进程场景可以直接使用包级方法：

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

初始包级默认实例使用 Worker ID `0`，适合单进程。多进程部署时，在第一次调用任何包级
生成或提取方法前安装受管默认实例。

### 独立 Generator

依赖注入、自定义布局或由应用分配 Worker ID 时，显式创建 Generator：

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

这个布局支持 256 个 Worker、每个 Worker 16 个本地分片、每个分片每毫秒 1,024 个 ID。

### Context、字符串与解析

```go
id, err := zid.NextIDContext(requestContext)
if err != nil {
    // context 取消/超时，或 zid.ErrClosed
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

`ParseHex`、`ParseBase36` 和 `ParseBase62` 会拒绝空值、非法字符、负数与溢出值。
字符串生成会分配内存，整数 `NextID` 热路径不会。所有需要提取 ID 字段的服务必须使用
与生成端一致的纪元和位布局。

Base62 使用按 ASCII 排序的 `0-9A-Za-z` 字母表。同长度编码按字节比较时保持数值顺序；
由于编码长度可变，不同长度之间的字典序不等同于数值顺序。

## 工作机制

### ID 布局

`zid` 把非负 ID 存储在有符号 `int64` 中：

```text
  63 个可用 bit
┌──────────────────────┬────────────┬───────────┬────────────┐
│ 时间戳（至少 41 bit） │ Worker (w) │ 分片 (s)  │ 序列号 (q) │
└──────────────────────┴────────────┴───────────┴────────────┘
                         w + s + q <= 22 bit
```

时间戳表示从协议纪元开始经过的真实毫秒数。默认协议如下：

| 属性 | 默认值 |
|---|---:|
| 协议纪元 | `2026-08-01 00:00:00 UTC` |
| Unix 毫秒值 | `1785542400000` |
| Worker 位 | 4 |
| 本地分片位 | 0 |
| 序列号位 | 18 |
| Worker 数量 | 16 |
| 每个 Worker 每毫秒容量 | 262,144 |
| 最短可用年限 | 约 69.7 年 |

ID 按数值排序时具有毫秒级时间顺序。使用多个 Worker 或本地分片时，同一毫秒内不保证
严格的全序生成顺序。

### 生成路径

1. 选择一个本地分片。只有一个分片时直接使用；多个分片时分散锁竞争。
2. 检查 Generator 未关闭；受管模式还会检查租约仍处于本地安全窗口内。
3. 读取真实毫秒时间；进入新毫秒后重置当前分片序列号。
4. 时钟回拨时固定在已经真实观察到的最后一个毫秒，消耗其剩余序列，但不会继续人为递增
   逻辑毫秒。
5. 当前分片容量耗尽后，先尝试所有其他分片。
6. 所有分片都满时，一个 goroutine 等待真实时钟前进；其余调用等待共享通知，不各自
   创建轮询 Timer。

这样既避免生成未来时间戳，又能保证唯一性。对应的取舍是：真实时间容量确实耗尽时，
系统会产生有意的背压。

### 唯一性范围

| 构造方式 | 保证范围 | 调用方责任 |
|---|---|---|
| 初始包级默认实例 | 单个进程生命周期 | 不要让多个进程同时使用默认 Worker `0` |
| `zid.New` | 单个 Generator 生命周期 | 为并发 Generator/进程分配互不相同的 Worker ID |
| `zid.NewManaged` | 在协调存储前提成立时保证分布式唯一 | 安全运行 Redis/Kubernetes，并确保有空闲 Worker ID |

进程退出后，以同一个 Worker ID 重建普通 Generator，可能重复最后一个毫秒的序列。
如果唯一性需要跨进程重启，且不希望业务自行实现 fencing，应使用受管模式。

## 分布式 Worker ID

Redis 和 Kubernetes Manager 是独立模块，应用只需引入实际使用的协调存储：

```shell
go get github.com/zohu/zid/zidredis@latest
go get github.com/zohu/zid/zidk8s@latest
```

### Redis

一步式启动 API 会创建 Manager，并安装为包级默认 Generator：

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

    // 应用关闭时调用 zid.CloseDefault。
    return nil
}
```

必须在启动阶段、任何包级生成或提取方法调用前，只调用一次 `ConfigureDefault`。之后再配置
会返回 `zid.ErrDefaultAlreadyUsed`。需要显式 `zid.NewManaged` Generator 时，可以使用
没有全局副作用的 `zidredis.New`。

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

Redis 使用随机 owner token 和 `SET NX PX` 获取租约。续租通过 Lua 原子比较 owner 后
执行 `PEXPIRE`，旧 owner 无法续租新 owner 的 key。关闭租约不会立即删除 key，剩余 TTL
作为 Worker ID 复用隔离期。

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

默认从环境变量读取 `NAMESPACE` 和 `POD_UID`，并使用集群内配置；也可以在
`zidk8s.Options` 中显式设置。

最小 namespace RBAC：

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

每个 Kubernetes HolderIdentity 都包含随机实例 token；抢占与续租使用 `resourceVersion`
进行 compare/update。非 `NotFound` API 错误不会被误判为 Lease 不存在，关闭后的 Lease
会保留到过期，形成复用隔离期。

### 租约生命周期与恢复

两个 Manager 的默认值一致：

| 参数 | 默认值 |
|---|---:|
| Lease TTL | 30 秒 |
| 续租间隔 | 10 秒 |
| 安全余量 | 10 秒 |
| 单次操作超时 | 3 秒 |

本地安全截止线保守地从协调请求开始时计算。续租与恢复状态如下：

| Generator 状态 | 可以发号？ | 行为 |
|---|---:|---|
| `GeneratorHealthy` | 是 | 租约有效，续租正常 |
| `GeneratorDegraded` | 是 | 本次续租临时失败，但尚未到达本地安全截止线 |
| `GeneratorRecovering` | 否 | 旧租约已 fencing，正在申请不同的 Worker ID |
| `GeneratorClosed` | 否 | Generator 已主动关闭 |

readiness 和监控可以使用 `State` 或包级 `DefaultState`。`Healthy` 表示“当前可以安全
发号”，因此 degraded 状态下仍返回 true；协调存储健康度也需要影响 readiness 时，应
检查完整状态。

永久失租后，Generator 会：

1. 在租约进入不安全状态前停止发号。
2. 在当前 Generator 剩余生命周期中永久排除该 Worker ID。
3. 以 100ms 到 5s 的指数退避持续申请。
4. 在所有本地分片上原子安装另一个 Worker ID。
5. 新租约安全后唤醒被阻塞的调用。

直接调用 `NextID`、`NextString`、`NextHex`、`NextBase36` 或 `NextBase62` 时，恢复期间
会等待，而不是返回可能重复的 ID。请求必须有截止时间时使用 `NextIDContext`。

协调存储暂时不可用或没有合格的空闲 Worker ID 时，Generator 会持续恢复，并在安全租约
可用后自动继续发号。

## 配置

### Generator Options

| 字段 | 零值/默认行为 | 范围或说明 |
|---|---|---|
| `BaseTime` | `2026-08-01 UTC` | 必须大于 0，且不能晚于当前时间 |
| `WorkerID` | `0` | 必须能放入 `WorkerIDBits`；受管模式自动分配 |
| `WorkerIDBits` | `4` | 0–19 bit |
| `ShardBits` | `0` | 0–16 bit |
| `SequenceBits` | `18` | 3–22 bit |
| `DisableWorkerID` | `false` | 不能与 `WorkerID` 或 `WorkerIDBits` 同时使用 |

必须满足 `WorkerIDBits + ShardBits + SequenceBits <= 22`。减少低位总数可以增加时间戳
寿命；重新分配低位则是在 Worker 数量、本地并发度和单分片容量之间取舍。

不需要 Worker 身份时可以完全移除 Worker 位：

```go
generator, err := zid.New(zid.Options{
    DisableWorkerID: true,
    ShardBits:       4,
    SequenceBits:    18,
})
```

## 安全与可用性行为

当生成过程暂时无法安全继续时，`zid` 通过明确的背压来保持唯一性：

| 条件 | 直接 `Next...` 方法 | `NextIDContext` |
|---|---|---|
| 仍有容量 | 立即返回 | 立即返回 |
| 当前真实毫秒内所有分片都满 | 等待真实下一毫秒 | 等待或返回 context 错误 |
| 受管租约正在恢复 | 等待另一个安全 Worker ID | 等待或返回 context 错误 |
| Generator 已关闭 | 以 `zid.ErrClosed` panic | 返回 `zid.ErrClosed` |

`Close` 可以重复调用。关闭后继续调用直接方法属于生命周期编程错误，因此会 panic。

### 分布式前提

Managed 唯一性依赖以下全部条件：

- Redis 或 Kubernetes 保留已确认的租约写入，并提供可靠的 compare/update 语义。Redis
  故障切换如果丢失已确认写入，可能破坏排他性。
- 节点间最大时钟偏差和协调存储最坏请求延迟小于配置的 `SafetyMargin`。
- 共享同一 Redis prefix 或 Kubernetes Lease prefix 的实例使用一致的 TTL 和安全参数。
- Worker ID 空间中存在合格的空闲租约。恢复中的 Generator 永不复用自己曾经失去的
  Worker ID，因此连续永久失租可能耗尽候选集合。
- 所有生成端与解析端使用相同的纪元和位布局。
- 尚未耗尽有符号 `int64` 时间戳范围。

发生网络分区或协调存储故障时，`zid` 优先保持唯一性，并在能够确认安全租约后自动恢复。

## 与其他 Go ID 库的对比

这些库解决的问题不同。下表比较协议和运维属性，不拿不同机器上的 benchmark 数字做横向
性能结论。

| 库 | ID 形态 | 时间排序 | 唯一性/协调模型 | 更适合的场景 |
|---|---|---|---|---|
| **zid** | 63-bit 正 `int64`；十进制、Hex、Base36、Base62 | 毫秒级数值排序 | 排他 Worker 内确定性序列；内置可选 Redis/Kubernetes 租约 | 需要分布式 fencing 的紧凑数据库主键 |
| [bwmarrin/snowflake](https://github.com/bwmarrin/snowflake) | 63-bit `int64`；默认 41 时间 + 10 节点 + 12 序列位 | 毫秒级数值排序 | 应用必须保证节点号唯一 | 由外部管理节点的经典 Snowflake |
| [Sonyflake](https://github.com/sony/sonyflake) | 63-bit 整数；默认 39 时间 + 8 序列 + 16 机器位 | 默认 10ms | Machine ID 回调或主机派生默认值；可选校验 | 更长寿命或更多机器编号 |
| [ULID](https://github.com/oklog/ulid) | 128-bit；标准 26 字符 Base32 | 毫秒级字典序；可选单调熵 | 随机熵、免协调、概率唯一 | 需要通用可排序字符串 |
| [KSUID](https://github.com/segmentio/ksuid) | 160-bit；标准 27 字符 Base62 | 秒级字典序 | 128-bit 随机载荷、免协调、概率唯一 | 需要强抗碰撞能力的通用字符串 |

以下需求通常更适合 `zid`：

- 数据库和 API 受益于 8 字节数字主键；
- Worker 所有权需要显式、可观测，而不是从主机元数据推断；
- 希望租约续期、fencing 和自动换 Worker 只有一套经过测试的实现；
- 相比概率碰撞模型，更希望得到明确的每 Worker 确定性容量；
- 本地高并发不能全部串行在一个 Generator 互斥锁上。

选型补充：不可预测是安全要求时，应配合密码学随机令牌；跨语言标准字符串或免协调生成
优先于紧凑数字存储时，可选择 ULID、KSUID 或
[UUIDv7](https://www.rfc-editor.org/rfc/rfc9562.html#name-uuid-version-7)；需要严格全局全序时，
应使用分布式序列服务。

## 性能

以下结果来自 Apple M1 Max、Go 1.26.2，只表示本项目实测，不代表跨库性能结论：

| Benchmark | 典型耗时 | 约合吞吐 | 内存分配 |
|---|---:|---:|---:|
| `BenchmarkGeneratorSingle` | 55–59 ns/op | 1700–1800 万 ID/s | 0 B/op，0 allocs/op |
| `BenchmarkPackageNextID` | 55–57 ns/op | 1700–1800 万 ID/s | 0 B/op，0 allocs/op |
| `BenchmarkGeneratorShardedParallel` | 33–36 ns/op | 2800–3000 万 ID/s | 0 B/op，0 allocs/op |

CPU 实测吞吐和协议容量是两个不同上限。默认格式每个 Worker 每毫秒有 262,144 个槽位；
上表测量的是当前实现在一台机器上的实际生成速度。

本机复现：

```shell
go test -run '^$' \
  -bench 'Benchmark(GeneratorSingle|PackageNextID|GeneratorShardedParallel)$' \
  -benchmem -count=8
```

## 开发

验证整个 workspace：

```shell
go test -race ./...
go vet ./...

GOWORK=off go -C zidredis test -race ./...
GOWORK=off go -C zidredis vet ./...

GOWORK=off go -C zidk8s test -race ./...
GOWORK=off go -C zidk8s vet ./...
```

当前语句覆盖率约为：核心模块 85.7%、Redis 模块 86.6%、Kubernetes 模块 81.9%。

## 贡献

涉及协议、时钟、并发或租约生命周期的贡献应包含针对性测试。修改 ID 协议或分布式安全
模型前，建议先创建 issue 讨论边界。

## 维护者发布

三个 module 从同一个 commit 打标签。两个子模块已依赖目标根版本，且所有改动已提交、
工作树干净后执行：

```shell
mise release v1.0.1
```

该任务会独立对三个 module 执行 `go mod tidy -diff`、`go vet` 和 race 测试，创建
`v1.0.1`、`zidredis/v1.0.1`、`zidk8s/v1.0.1` 三个标签，再原子推送分支与标签。

## License

[MIT](LICENSE)

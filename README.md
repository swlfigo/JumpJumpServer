# JumpJumpServer

JumpJump 跳一跳的联机对战服务器（Go）。

> **探索阶段**：对应 iOS 仓库 `JumpJump` 的 `explore/multiplayer` 分支，未来不一定采用或合并。

## 现状

| 模块 | 状态 |
| --- | --- |
| `internal/game` | 已完成：客户端 `GameLogic.swift` 的逐位一致移植（平台生成、跳跃轨迹、落点判定、计分） |
| `internal/server` | 已完成 v0：房间、WebSocket、服务器权威判定、作弊校验、挂机判负、断线出局、排名 |
| `cmd/jumpjump-server` | 已完成：命令行入口、Bonjour 广播 |
| iOS 客户端联机 | 未开始 |

协议见 [docs/protocol.md](docs/protocol.md)。

## 运行

```sh
go run ./cmd/jumpjump-server                  # 默认 :7788，路径 /ws
go run ./cmd/jumpjump-server -min-players 1   # 单人调试
```

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-addr` | `:7788` | 监听地址 |
| `-bonjour` | `true` | 在局域网广播 `_jumpjump._tcp` |
| `-idle` | `1m0s` | 多久不起跳判挂机出局 |
| `-min-players` | `2` | 全员准备后开局所需人数 |
| `-max-players` | `4` | 房间上限 |

启动日志会打印可连接的 `ws://IP:端口/ws`。手机和电脑需在同一 Wi-Fi 下。

## 测试

```sh
go test ./...
```

`internal/game/testdata/swift_vectors.json` 是从客户端 Swift 代码导出的对拍数据（浮点数以 IEEE-754 位模式保存，逐位比较）。修改客户端 `GameLogic.swift` 后必须重新生成并跑测试：

```sh
./scripts/gen-swift-vectors.sh   # 默认读取同级目录 ../JumpJump，可用 IOS_DIR 指定
```

需要在 Apple Silicon Mac 上生成，浮点行为才与 iPhone 一致。

## 浮点一致性约定

服务器是落点与分数的唯一权威，客户端本地预测必须和服务器算得一模一样：

- 全部使用 `float32`，与客户端的 `Float` 一致。
- 乘积参与加减时必须用 `float32()` 包裹。Go 在 arm64 上会把 `x*y+z` 合并成 FMA 指令，Swift 不会；去掉任何一处包裹，对拍测试都会失败。
- `min`/`max` 使用 `swiftMin`/`swiftMax`，保持 Swift 对 NaN 和 ±0 的语义。
- 客户端不能使用 `simd_normalize`：它在 ARM 上用 rsqrt 近似，Go 无法逐位复现。iOS 分支已改为 `delta / simd_length(delta)`。

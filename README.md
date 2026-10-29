# JumpJumpServer

JumpJump 跳一跳的联机对战服务器（Go）。

> **探索阶段**：对应 iOS 仓库 `JumpJump` 的 `explore/multiplayer` 分支，未来不一定采用或合并。

## 现状

| 模块 | 状态 |
| --- | --- |
| `internal/game` | 已完成：客户端 `GameLogic.swift` 的逐位一致移植（平台生成、跳跃轨迹、落点判定、计分） |
| `internal/server` | 已完成 v0：房间列表与房间密码、WebSocket、服务器权威判定、作弊校验、挂机判负、断线出局、排名 |
| `cmd/jumpjump-server` | 已完成：命令行入口、Bonjour 广播 |
| `cmd/jumpbot` | 已完成：练习用机器人对手 |
| iOS 客户端联机 | 已完成 v0（iOS 仓库 `explore/multiplayer` 分支） |

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
| `-ready-timeout` | `15s` | 在房间里多久不准备就被移出（最后 5 秒倒计时提醒） |
| `-min-players` | `2` | 全员准备后开局所需人数 |
| `-max-players` | `4` | 房间上限 |

启动日志会打印可连接的 `ws://IP:端口/ws`。手机和电脑需在同一 Wi-Fi 下。

### 机器人对手

一个人测试时，可以让机器人先开一个房间等你，房间列表里显示为「Bot 的房间」：

```sh
go run ./cmd/jumpbot -name Bot                    # 一直跳到失误为止
go run ./cmd/jumpbot -name Bot -jumps 8           # 跳 8 次后故意掉落
go run ./cmd/jumpbot -name Bot -password 1234     # 加锁的房间，凭密码 1234 进入
```

机器人每局结束后自动准备，可以连续再来一局。`-error` 调整按压误差（毫秒，默认 40）；`-rejoin 20s` 让它每局结束就离开房间、20 秒后再开一个新房间，用来测"对手走了"的情况。

### iOS 联机 UI 测试

```sh
go run ./cmd/jumpjump-server &
go run ./cmd/jumpbot -name Bot -jumps 8 &
go run ./cmd/jumpbot -name LeaveBot -jumps 6 -rejoin 20s &
go run ./cmd/jumpbot -name LockBot -password 1234 &
cd ../JumpJump
TEST_RUNNER_JUMPJUMP_ONLINE_TEST=1 xcodebuild test -project JumpJump.xcodeproj -scheme JumpJump \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro' \
  -only-testing:JumpJumpUITests/JumpJumpUITests/testOnlineRaceAgainstBot \
  -only-testing:JumpJumpUITests/JumpJumpUITests/testOnlineDisablesAssist \
  -only-testing:JumpJumpUITests/JumpJumpUITests/testOnlineRematchAloneReturnsHome \
  -only-testing:JumpJumpUITests/JumpJumpUITests/testOnlineReadyTimeoutLeavesRoom \
  -only-testing:JumpJumpUITests/JumpJumpUITests/testOnlineResultTimeoutReturnsHome \
  -only-testing:JumpJumpUITests/JumpJumpUITests/testOnlineLockedRoomNeedsPassword
```

测试覆盖 Bonjour 发现、房间列表、创建房间、加锁房间输错/输对密码、离开房间回到列表、开局、起跳得分、本地预测与服务器逐位一致、掉落观战、最后一人掉落动画结束后才出结算、再来一局、对手离开后再来一局回到首页、房间里或结算后迟迟不准备被移出、退出联机，以及联机时辅助功能不可用。

另有一个测试 `testOnlineWithoutServerExplains` 检查局域网里没有服务器时大厅会说明原因，要在**没有任何 jumpjump-server 运行**时单独跑，用 `TEST_RUNNER_JUMPJUMP_NO_SERVER_TEST=1` 代替上面的环境变量。

如果 7788 端口已经有服务器在跑（比如你正在手动测试），可以另开一个不广播的私有服务器给测试用，并让测试直接填地址：

```sh
go run ./cmd/jumpjump-server -addr :7790 -bonjour=false &
go run ./cmd/jumpbot -url ws://127.0.0.1:7790/ws -name Bot -jumps 8 &
go run ./cmd/jumpbot -url ws://127.0.0.1:7790/ws -name LeaveBot -jumps 6 -rejoin 20s &
go run ./cmd/jumpbot -url ws://127.0.0.1:7790/ws -name LockBot -password 1234 &
# 上面的 xcodebuild 命令前再加 TEST_RUNNER_JUMPJUMP_SERVER=127.0.0.1:7790
```

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

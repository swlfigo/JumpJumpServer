# 联机协议草案 v0

> 探索阶段，随实现调整。

## 模式

同一赛道竞速：房间内所有人同时开跑、跳同一串平台，互相可见；各自视角跟随自己。全员出局后按规则排名。

## 传输

- WebSocket，消息为 JSON：`{"type": "...", ...}`，路径 `/ws`，默认端口 7788。
- 局域网：`ws://`，服务器通过 Bonjour 广播 `_jumpjump._tcp`，TXT 记录：

  | 键 | 示例 | 说明 |
  | --- | --- | --- |
  | `v` | `0` | 协议版本 |
  | `path` | `/ws` | WebSocket 路径 |
  | `port` | `7788` | 端口 |
  | `addrs` | `192.168.1.10,192.168.1.11` | 服务器的局域网 IPv4，逗号分隔 |

  客户端用 `NWBrowser` 发现服务后，**直接按 `addrs` 里的地址依次连接** `ws://<addr>:<port><path>`，不要解析服务的主机名：广播库不会否定应答 IPv6（AAAA）查询，系统解析器会白等约 5 秒。
- 也可以手动输入服务器启动日志里打印的 `ws://IP:端口/ws`。
- 公网（以后）：`wss://`，Bonjour 换成房间码。

## 原则：服务器权威

客户端**只发输入**（按压毫秒数），坐标、落点结果、分数全部由服务器用 `internal/game` 计算后广播。客户端松手后本地预测播放动画，以服务器的 `jumped` 为准（两端算法逐位一致，正常情况下不会出现修正）。

## 消息

客户端 → 服务器：

| type | 字段 | 说明 |
| --- | --- | --- |
| `join` | `name`, `roomCode` | 进入房间 |
| `ready` | | 准备 |
| `charge` | `seq` | 开始蓄力（仅用于让别人看到下蹲动画） |
| `jump` | `seq`, `pressMs` | 松手起跳；`seq` 为第几跳 |
| `leave` | | 主动离开 |

服务器 → 客户端：

| type | 字段 | 说明 |
| --- | --- | --- |
| `welcome` | `playerID`, `players` | 加入成功；`playerID` 由服务器分配并绑定连接 |
| `lobby` | `players: [{playerID, name, ready}]` | 房间成员或准备状态变化；对局结束后也会发一次（全员回到未准备） |
| `start` | `seed`, `countdownMs` | 倒计时后开跑；客户端用 `seed` 生成平台（Go 生成器已与 Swift 逐位一致） |
| `charge` | `playerID`, `seq` | 转发蓄力 |
| `jumped` | `playerID`, `seq`, `pressMs`, `landingBits`, `result`, `score`, `combo` | 权威结果；`landingBits` 为落点 x,z 的 float32 位模式，避免十进制往返误差 |
| `idleWarning` | `secondsLeft` | 只发给即将被判挂机的玩家 |
| `out` | `playerID`, `reason` | `fell` / `idle` / `disconnected` / `left` |
| `result` | `ranking: [{playerID, score, status}]` | 全员出局后的排名 |
| `error` | `code`, `message` | 被拒绝的消息 |

## 房间流程

1. 连接后第一条消息必须是 `join`，否则断开。房间码相同即进入同一房间，房间不存在时自动创建。
2. 房间人数达到下限（默认 2 人）且全员 `ready` 后发 `start`，倒计时（默认 3 秒）结束后开跑。
3. 对局中不能加入（`room_busy`），满员（默认 4 人）返回 `room_full`。
4. 全员出局后发 `result`，房间回到准备阶段，可以再来一局。

## 服务器校验

- 身份只认连接，忽略客户端自报的 ID；消息里多出的字段（坐标、结果、分数）一律忽略。
- `pressMs` 必须是 0–3000 的整数；`seq` 必须严格加 1。被拒绝的跳跃不生效，`seq` 不前进。
- 只有存活且站稳的玩家才能起跳：本次起跳到达时间 ≥ 上一跳落地时间（或开跑时间）+ 0.2s 落地缓冲 + 本次按压时长 − 网络抖动余量（默认 300ms）。
- 单条消息 ≤ 4KB，每秒最多 20 条；超限、JSON 格式错误（包括 `pressMs` 不是整数）直接断开。未知 `type` 回 `error` 后断开。
- 每 10 秒 ping 一次，无响应按断线处理。

错误码：`not_joined`、`already_joined`、`bad_name`、`bad_room_code`、`room_busy`、`room_full`、`not_in_lobby`、`not_racing`、`bad_charge`、`bad_press`、`bad_seq`、`too_early`、`unknown_type`。

## 规则

- **挂机**：开跑后或上次起跳后 60 秒（服务器可配置）没有起跳，判 `idle` 出局；第 45 秒发 `idleWarning`。已出局观战的玩家不计时。
- **断线**：v0 不支持重连，断线立即判 `disconnected` 出局。
- **排名**：正常出局（`fell`）的玩家按分数排名；`idle` / `disconnected` / `left` 判负，排在所有正常玩家之后，越早判负越靠后。
- **辅助模式**：联机时禁用。

## 已知局限

自动瞄准脚本发送的是合法输入，服务器无法拦截。以后可做统计检测（中心命中率、按压误差分布），服务器保存每局完整输入以便回放审查。

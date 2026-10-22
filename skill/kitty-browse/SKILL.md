---
name: kitty-browse
description: 通过 kitty-browse 自有 Unix socket 原生操作其 Chromium tabs；不依赖 Node 或外部 browser helper。
---

# kitty-browse workflow

使用 `kitty-browse` 二进制及其启动的 Chromium。不得调用、安装或转发到 `terminal-browser`、`agent-browser`、Node.js、Playwright helper；CLI 只是原生 typed API 的可选客户端。完整契约见 `docs/action-api.md`。

## 固定流程

1. **启动已知实例**
   - 持久会话：指定专用 `-profile-dir` 和 `-control-socket`。
   - 临时会话：指定 `-private` 和 `-control-socket`，不要同时给 profile。
   - 不隐式启动第二个浏览器，不开放 CDP TCP。

   ```sh
   kitty-browse -profile-dir "$PROFILE" -control-socket "$SOCKET" "$URL"
   # 或：kitty-browse -private -control-socket "$SOCKET" "$URL"
   ```

2. **discover + pin**
   - `GET /v1/info`，确认 `apiVersion=v1`、`backend=native-cdp`。
   - 从 `tabs` 选 tab；保存该次发现的 `browserId + tab.id + tab.targetId`。
   - 后续每个 request 显式携带三者。ID 失效就重新 info，绝不回退到 active tab 或展示序号。
   - 直接把 `capabilities[].inputSchema` 映射为 golder 工具参数，并保留 `readOnly` 标记。

3. **先读语义树**
   - 对目标 tab 调 `snapshot`，通常 `{"interactive":true,"compact":true}`（CLI：`snapshot -i -c`）；该模式默认省略可能很大的整页 text。
   - 从 accessibility node 选择 `@eN`；不要先截图再猜坐标。

4. **执行一个小动作**
   - click：`{"action":"click","params":{"ref":"@e12"}}`
   - fill：`{"action":"fill","params":{"ref":"@e9","text":"..."}}`
   - key：`{"action":"press","params":{"key":"Enter"}}`
   - 每次使用唯一字符串 `id`，设置合理 `timeoutMs`，target 始终 pin。

5. **动作后核验**
   - 导航、提交、打开 dialog 或 DOM 大改后重新 `snapshot`；旧 ref 只属于原 tab 和原 navigation epoch。
   - 简单导航核验可用 `get {"kind":"url"}`；业务结果应用 snapshot/get 读取确认。
   - mutating action 超时/取消且 `error.details.mayHaveExecuted=true` 时，先核验，禁止盲重试。

6. **结束**
   - 调 `done {}`，确认 `result.released=true`。它释放 action mode，不关闭浏览器。

## 最小 typed 调用

```sh
curl --silent --show-error --unix-socket "$SOCKET" http://unix/v1/info

curl --silent --show-error --unix-socket "$SOCKET" \
  -H 'Content-Type: application/json' --data-binary @- \
  http://unix/v1/actions <<'JSON'
{"apiVersion":"v1","id":"golder-1","action":"snapshot","params":{"interactive":true,"compact":true},"target":{"browserId":"BROWSER_ID","tabId":"TAB_ID","targetId":"TARGET_ID"},"timeoutMs":30000}
JSON
```

如果必须用 CLI，flags 放在 `--` 前：

```sh
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- snapshot -i -c
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- click @e12
```

CLI `-tab 2` 只会在调用前把当时的 1-based 展示序号解析为稳定 ID；不要保存或传播这个序号。含密码/token/cookie 的 raw request 必须走 stdin：

```sh
kitty-browse action -socket "$SOCKET" -request-json - < "$PRIVATE_REQUEST_FILE"
```

不要把秘密放到 argv、workspace、日志或最终回答。

## 人在回路与写操作

- 看到 SSO、二维码、设备确认、MFA、授权/consent 时停下，让用户在 pane 中扫码、登录或批准；不要截图/保存/打印二维码。
- 不自动提交购买、发帖、发送消息、删除、发布、转账、授权、账户设置等业务写入。先展示将发生的动作并等待用户明确批准。
- 登录字段也按最小权限处理；不要通过 `eval` 绕过用户可见的认证流程。
- `cookies.set/clear`、`storage.set/clear`、navigate/tab/viewport、eval 和所有输入动作都按 mutating 处理。
- `console/errors/network.requests` 虽标记 readOnly，但 `clear:true` 会清空内存观察记录；默认不 clear。

## 截图规则

- DOM 表单、链接和按钮：用 snapshot/ref，不需要截图。
- 仅在 canvas、图表、纯视觉状态、二维码需由用户直接查看、或最终视觉验证时使用截图。
- 优先让用户看 pane。若程序必须分析，优先 inline `result.data` base64 并仅在内存消费。
- private 模式不会禁止显式 artifact 落盘。敏感内容不得传 `path`，不得保存到 workspace；非敏感落盘也要使用新路径并清理。

## Parity 自检矩阵

同级 `terminal-browser/skill` 只能作为只读 workflow 参考，不能作为外部 helper 或兼容性承诺。每次计划前按 `/v1/info` 复核：

| 需求 | kitty-browse 当前状态 | 采用方式 |
|---|---|---|
| 发现 tabs/capabilities | 原生支持 | `/v1/info`，稳定 ID pin |
| AX snapshot/ref | 原生支持 | `snapshot -i`，ref 绑定 tab+epoch |
| click/fill/type/key/select/upload | 原生支持 | typed action；动作后复读 |
| get/is/find/wait/scroll | 原生支持 | schema 参数，不猜其他工具语法 |
| tab/nav/viewport | host 原生支持 | stable target；viewport 仅 active tab |
| network/console/errors | 有界内存观察 | 无 headers/request body；非路由器 |
| cookie/storage | 当前 URL/origin 支持 | 视为敏感；写操作需批准 |
| screenshot/PDF | inline base64 或新 path | 仅视觉/artifact 场景 |
| eval | 原生支持 | 始终视为 mutating，不作只读推断 |
| record/trace/HAR | 未实现 | 不伪造、不调用 helper |
| network routing/mock/intercept | 未实现 | 只有观察和 response body 读取 |
| device/touch/geolocation 等仿真 | 未实现 | 不宣称支持 |
| closed shadow 穿透 | 未实现 | 不用 eval 冒充通用支持 |
| OOPIF | 原生支持 | 严格 frame/target 关联；pointer 命中无法确认时 fail closed |
| dialog | 原生支持 | status/accept/dismiss，prompt 内容不记日志 |
| popup | 原生观察 | `wait popup` 后重新 info；popup 由 host 纳管 |
| download | 原生 browser-scoped | `download wait/list`，只返回安全路径和 metadata |
| 完整 agent-browser CLI 兼容 | 未实现且非目标 | 只用 capability schema |

若 `/v1/info` 未列出所需 action，明确报告“不支持”；不要尝试 helper passthrough，也不要宣称全量 parity。

# kitty-browse 原生 Action API（v1）

本文档描述当前 `kitty-browse` 二进制直接提供的自动化契约。实现是进程内的 Chromium/CDP 会话，不启动、调用或转发到 `terminal-browser`、`agent-browser`、Node.js 或其他 helper。CLI 只是此契约的一个客户端；未来 golder 内置工具应直接从 `/v1/info` 的 capability schema 映射工具，而不是把 CLI 文本命令当成唯一协议。

## 1. 启动、传输与安全边界

启动一个已知 profile 和已知 socket 的浏览器实例：

```sh
kitty-browse \
  -profile-dir "$HOME/.config/kitty-browse/profiles/work" \
  -control-socket "$XDG_RUNTIME_DIR/kitty-browse-work.sock" \
  https://example.com
```

临时会话可改用 `-private`，且不能同时指定 `-profile-dir`：

```sh
kitty-browse -private \
  -control-socket "$XDG_RUNTIME_DIR/kitty-browse-private.sock" \
  https://example.com
```

- 控制面只监听 Unix-domain socket。socket 创建为 owner-only `0600`；新建的直接父目录为 `0700`，已存在父目录的权限不会被修改。
- API 不发布 CDP endpoint，也没有 CDP TCP 监听。不要另开远程调试端口来绕过此 API。
- socket 路径已存在时启动失败，不会删除或替换现有条目；关闭时只删除服务器自己创建且仍为同一文件的 socket。
- socket 文件权限就是访问控制。不要把 socket 放到其他用户可访问的目录，也不要把它转发到 TCP。
- `-private` 使用 Chromium 临时 user-data-dir，并使用内存 history；退出时不保留该 profile/history。但网络、console 和 page error 观察缓冲在浏览器存活期间仍位于内存中（每 tab 最多 500 个 request、200 条 console、200 条 error；不保存请求 headers/body）。
- private 模式禁用 `KITTY_BROWSE_DEBUG` 文件日志。非 private 模式若设置该环境变量，会以 `0600` 追加 debug 日志；日志和显式 artifact 是独立的数据泄露边界。
- private 模式并不阻止调用方写文件：带 `path` 的 `screenshot`/`pdf`、`kitty-browse screenshot` 和界面截图热键仍会落盘。敏感二维码、一次性码和认证画面不要保存到 workspace，也不要把 inline base64 或页面内容打印进日志；优先让用户直接在 pane 中扫码/确认。

下文用 curl 展示 HTTP-over-Unix-socket；golder 可使用任何支持 Unix socket HTTP 的客户端：

```sh
SOCKET="$XDG_RUNTIME_DIR/kitty-browse-work.sock"
curl --silent --show-error --unix-socket "$SOCKET" http://unix/v1/info
```

## 2. 路由

| 路由 | 方法 | 返回 | 说明 |
|---|---|---|---|
| `/v1/info` | `GET` | JSON | 浏览器身份、当前 tabs 和可机读 capabilities |
| `/v1/actions` | `POST` | JSON | 执行一个 typed action；body 上限 1 MiB |
| `/v1/screenshot` | `GET` | `image/png` | 当前 active tab 的 viewport PNG；`?fullPage=true` 捕获整页 |
| `/screenshot` | `GET` | `image/png` | 保留的兼容别名，行为同 `/v1/screenshot` |
| `/v1/quit` | `POST` | JSON | 优雅关闭该浏览器；成功响应在 socket 关闭前写出 |

路径必须精确匹配。截图 query 只允许恰好一个 `fullPage=true|false`；它没有 target 参数，需选择指定 tab 时应使用 typed `screenshot` action。截图 GET 的服务端时限为 15 秒；JSON API 外层服务端时限为 120 秒，JSON response 上限 16 MiB。

不支持的方法返回 `405` 和 `Allow`。JSON action 的应用级失败通常仍返回 HTTP `200`，应检查 `ok`；畸形 JSON、过大 body 或外层超时等传输/入口错误使用非 2xx 状态及精简错误 envelope。

`/v1/quit` 与 `/v1/actions` 共用同一套授权：socket 文件本身是唯一凭据（`0600`，父目录不可被他人写入），没有额外 token 或路径前缀。响应固定为

```json
{"apiVersion":"v1","id":null,"ok":true,"result":{"quitting":true}}
```

服务端先写出并 flush 该响应，再开始拆除（关闭 socket、结束 Chromium），所以调用方总能收到答复；之后对该路径的请求会失败。该路由只在宿主启用时存在（本仓库的 kite-browse pane 与 browserhost 都启用），未启用时返回 `404`，非 `POST` 返回 `405`。父进程（例如 golder）应当用它关闭自己启动的浏览器，而不是向 pane 注入按键或按可执行文件名猜测进程。

## 3. 发现与 target pinning

`GET /v1/info` 返回：

```json
{
  "apiVersion": "v1",
  "browserId": "browser-kb-...",
  "activeTabId": "0123...",
  "backend": "native-cdp",
  "tabs": [
    {
      "id": "0123...",
      "targetId": "0123...",
      "url": "https://example.com/",
      "title": "Example",
      "index": 1,
      "active": true
    }
  ],
  "capabilities": [
    {
      "name": "snapshot",
      "description": "Accessibility snapshot with stable element refs.",
      "readOnly": true,
      "inputSchema": {
        "type": "object",
        "properties": { "interactive": { "type": "boolean" } },
        "additionalProperties": false
      }
    }
  ]
}
```

实际 `inputSchema` 会包含每项完整 JSON Schema。推荐流程：

1. 调用 `/v1/info`，确认 `apiVersion == "v1"` 和 `backend == "native-cdp"`。
2. 按 action 名称查 capability，并直接用 `inputSchema` 生成/校验工具参数；以 `readOnly` 决定批准和重试策略。
3. 从 `tabs[]` 选择目标，并把发现到的 `browserId`、`id`、`targetId` 三者 pin 到 request。
4. target 关闭或 ID 不再匹配时重新发现；绝不能回退到 active tab、tab 顺序或“最接近”的 target。

`tabs[].id` 和 `targetId` 都是不透明稳定 ID。当前实现中二者来自同一个 Chromium target ID，但调用方不能依赖它们永远相等。`tabs[].index` 只是当前展示顺序，不属于稳定 API target。

CLI 的 `--tab 2` 是例外：CLI 先获取 `/v1/info`，把 1-based 展示序号临时解析成稳定 tab ID，再 pin 三个 ID 后发请求。服务端 target 本身不接受序号，也不会在 ID 失效时按序号回退。

省略 target 时，服务端会在接收请求时 pin 当时的 active tab；自动化客户端仍应显式 pin，以免“当前 tab”在发现与调用之间改变。`target.browserId` 不匹配，未知/已关闭 tab，或 `tabId` 与 `targetId` 不对应，均返回 `target_not_found`。

## 4. Request envelope

```json
{
  "apiVersion": "v1",
  "id": "golder-018f...",
  "action": "click",
  "params": { "ref": "@e12" },
  "target": {
    "browserId": "browser-kb-...",
    "tabId": "0123...",
    "targetId": "0123..."
  },
  "timeoutMs": 30000
}
```

| 字段 | 类型 | 契约 |
|---|---|---|
| `apiVersion` | string，可省略 | 省略即 v1 兼容写法；显式值必须恰为 `v1` |
| `id` | string | 调用方关联 ID；建议每次唯一。直接 API 当前允许空字符串，CLI 会补 `kb-...` ID |
| `action` | string | capability 的精确 `name`；必填且非空 |
| `params` | object/null，可省略 | action 参数；规范调用应满足对应 `inputSchema` |
| `target.browserId` | string，可省略 | 若提供必须匹配当前 browser |
| `target.tabId` | string，可省略 | `/v1/info` 给出的稳定 tab ID |
| `target.targetId` | string，可省略 | `/v1/info` 给出的 Chromium target ID |
| `timeoutMs` | integer，可省略 | `0`/省略为 30000；显式范围 1..120000 ms |

实现还接受 CLI parser 使用的 `args: string[]` 字段，但执行器不以它驱动行为；golder 不应依赖它。request envelope 使用严格字段解码，未知顶层字段会得到 `invalid_arguments`。`params` 必须是 object 或 null；capability schemas 声明 `additionalProperties:false`，客户端应据此先行校验（底层各 action 的解码并非都拒绝额外字段）。

直接 POST：

```sh
curl --silent --show-error --unix-socket "$SOCKET" \
  -H 'Content-Type: application/json' \
  --data-binary @- http://unix/v1/actions <<'JSON'
{"apiVersion":"v1","id":"golder-1","action":"get","params":{"kind":"url"},"target":{"browserId":"BROWSER_ID","tabId":"TAB_ID","targetId":"TARGET_ID"},"timeoutMs":30000}
JSON
```

示例中的占位 ID 必须替换为同一次 info 发现得到的值。

## 5. Response envelope、错误与重试

成功：

```json
{
  "apiVersion": "v1",
  "id": "golder-1",
  "ok": true,
  "result": { "ok": true },
  "error": null,
  "meta": {
    "browserId": "browser-kb-...",
    "tabId": "0123...",
    "targetId": "0123...",
    "url": "https://example.com/",
    "title": "Example",
    "backend": "native-cdp"
  }
}
```

失败：

```json
{
  "apiVersion": "v1",
  "id": "golder-2",
  "ok": false,
  "result": null,
  "error": {
    "code": "timeout",
    "message": "request timed out",
    "retryable": false,
    "details": { "mayHaveExecuted": true }
  },
  "meta": {
    "browserId": "browser-kb-...",
    "tabId": "0123...",
    "targetId": "0123...",
    "url": "https://example.com/",
    "title": "Example",
    "backend": "native-cdp"
  }
}
```

- 用 `ok` 分支，不用 HTTP 状态、`result` 的真假值或 message 文本分支。
- 错误机读字段是 `error.code`、`error.retryable`；执行不确定性位于 **`error.details.mayHaveExecuted`**，不是顶层字段。
- 常见 code 包括 `invalid_arguments`、`unsupported_version`、`unsupported_action`、`target_not_found`、`target_busy`、`not_found`、`ambiguous_locator`、`stale_ref`、`not_visible`、`javascript_error`、`timeout`、`canceled`、`request_not_found`、`unsupported_frame`、`target_not_active`、`last_tab`、`browser_unavailable`、`cdp_error`、`internal_error`。调用方必须允许未来出现其他 code。
- capability 的 `readOnly` 是重试/批准依据。只读 action 超时时实现可给 `retryable:true`；仍需重新发现并确认 target 后再试。
- click/fill/eval/navigate/tab/cookie/storage 等 mutating action 超时或取消时会强制 `retryable:false` 并给 `details.mayHaveExecuted:true`。这表示副作用可能已经发生；不得盲重试。先用 `snapshot`、`get`、`tab.list` 或业务读状态核验。
- `console --clear`、`errors --clear`、`network.requests --clear` 在 capability 中仍标记 `readOnly:true`，但会清除本地观察缓冲。需要保留诊断信息时不要传 `clear:true`。
- `screenshot`/`pdf` 也标记 `readOnly:true`，含义是不会修改页面业务状态；一旦提供 `path`，仍有“新建本地文件”的副作用。批准和清理 artifact 时必须单独考虑这一点。
- 入口层生成的精简错误（例如 body 非法或 120 秒外层超时）只有 `apiVersion/id/ok/error`，可能没有 `result/meta/details`。

## 6. Element refs 与 DOM 操作原则

`snapshot` 返回 accessibility tree、页面 `url/title/text`、`snapshotId` 及节点 `ref`（如 `@e12`）。ref：

- 只绑定产生它的 tab/session 和该 document 的 loader/navigation epoch；不能跨 tab 使用。
- 同一 document 的后续 snapshot 会尽量保持同一 backend node 的 ref，且 ref 名称不会回收。
- 导航/reload 后旧 ref 返回 `stale_ref`；节点 detached 也返回 `stale_ref`。
- 动作导致导航、弹窗或大幅 DOM 更新后，应重新 snapshot，不要缓存 ref 做长链操作。
- selector 必须唯一；0 个结果为 `not_found`，多个结果为 `ambiguous_locator`。role/name/text locator 通过 accessibility snapshot 匹配，也必须唯一才能用于动作。

截图不是一般 DOM 操作的必要入口。优先 `snapshot -i`/`snapshot {interactive:true}` 获取语义节点，用 ref click/fill，再 snapshot 或 `get {kind:"url"}` 验证。只在 canvas、二维码、纯视觉状态或最终视觉核验时截图。

## 7. 当前 capabilities

以下以 `/v1/info` 的 schema 为准。共同 locator 字段是：

```json
{
  "selector": "CSS selector",
  "ref": "@e12",
  "role": "button",
  "name": "Save",
  "text": "visible/accessibility text"
}
```

`ref` schema 要求以 `@` 开头。locator action 通常应只给一种定位方式。

### Snapshot、定位与输入

| action | readOnly | params / 实际行为 |
|---|---:|---|
| `snapshot` | yes | `interactive`, `compact`, `depth>=0`, `maxNodes>=1`, `includeText`；默认最多 5000 节点，密码值不输出。interactive/compact 默认省略可能很大的整页 `text`，按需显式开启 |
| `find` | yes | locator 字段加 `exact`; 返回 `{refs,count}` |
| `get` | yes | 必填 `kind`; 可带 locator 和 attr `name`。实际 kind：`title`,`url`,`text`,`html`,`count`,`value`,`box`,`attr`；`count` 要求 CSS selector |
| `is` | yes | 必填 `kind=visible|enabled|checked` 加 locator；返回 boolean |
| `click`, `dblclick`, `hover`, `focus` | no | locator |
| `check`, `uncheck` | no | locator |
| `fill` | no | locator 加 `text`（或别名 `value`）；清空/设值并发 input/change |
| `type` | no | locator 加 `text`（或 `value`）；聚焦后在当前值/光标处插入 |
| `select` | no | locator 加 `value` 或 `text` |
| `upload` | no | locator 加非空绝对/可访问文件路径数组 `files` |
| `press`, `keydown`, `keyup` | no | 必填 `key`；原生 key event |
| `keyboard.insert` | no | 必填 `text`；向已聚焦元素插入文本 |
| `wait` | yes | locator 加 `state=visible|hidden`、`timeoutMs>=0`；若无 locator 则仅等待该毫秒数，默认 30000 |
| `wait.url` | yes | `pattern` 必填；默认 substring，含 `*`/`?` 时按 glob，`exact:true` 时精确匹配；包含 SPA hash |
| `wait.function` | yes | `script` 必填；等待表达式或 Promise 结果为 truthy |
| `wait.loadState` | yes | `state=domcontentloaded|load`；不声明 networkidle |
| `wait.popup` | yes | 可选 `urlPattern,titlePattern,sinceToken`；只消费当前 opener 最近 5 秒或 token 后的一个 popup，返回 target metadata |
| `dialog.status` | yes | 返回当前阻塞 dialog 的 `open,type,message,defaultPrompt` |
| `dialog.accept`, `dialog.dismiss` | no | 接受/拒绝 dialog；accept 可带 `promptText`，该文本不保留或记录 |
| `scroll` | no | `deltaX`,`deltaY`，可选事件坐标 `x`,`viewportY`；实现也接受 `y` 作为 deltaY 别名 |
| `scrollintoview` | no | locator |
| `frame` | no | 接受 CSS `selector` 或 `main:true`。同进程 frame 用 isolated world；OOPIF 使用严格 frame/target/parent 关联和仅 detach 的子会话。OOPIF pointer 点击只有在 child-session 命中探针确认后才成功 |
| `eval` | no | 必填 `script`; await Promise，结果 by-value；始终按 mutating 处理 |

Typed JSON 示例（以下均应加已 pin 的 `target`、唯一 `id` 和合适 `timeoutMs`）：

```json
{"action":"snapshot","params":{"interactive":true,"compact":true,"maxNodes":1000}}
{"action":"click","params":{"ref":"@e12"}}
{"action":"fill","params":{"ref":"@e9","text":"Ada Lovelace"}}
{"action":"press","params":{"key":"Enter"}}
{"action":"get","params":{"kind":"value","ref":"@e9"}}
{"action":"get","params":{"kind":"url"}}
{"action":"eval","params":{"script":"document.querySelector('main')?.dataset.state"}}
{"action":"wait","params":{"role":"dialog","name":"Complete","state":"visible","timeoutMs":10000}}
{"action":"scroll","params":{"deltaY":500}}
```

CLI 等价示例仅用于人工调试，不替代 typed API：

```sh
kitty-browse action -socket "$SOCKET" -- snapshot -i
kitty-browse action -socket "$SOCKET" -- click @e12
kitty-browse action -socket "$SOCKET" -- fill @e9 'Ada Lovelace'
kitty-browse action -socket "$SOCKET" -- press Enter
kitty-browse action -socket "$SOCKET" -- get url
kitty-browse action -socket "$SOCKET" -- wait 500
kitty-browse action -socket "$SOCKET" -- scroll down 500
```

其余 parser 中已实现的 CLI 拼写：

```sh
kitty-browse action -socket "$SOCKET" -- eval 'document.title'
kitty-browse action -socket "$SOCKET" -- find role button --name Save --exact
kitty-browse action -socket "$SOCKET" -- is visible @e12
kitty-browse action -socket "$SOCKET" -- frame '#same-process-frame'
kitty-browse action -socket "$SOCKET" -- frame main
```

### Tab、导航与 viewport 生命周期

这些 action 由 kitty-browse UI host 执行，但使用同一个 typed envelope：

| action | readOnly | params / 约束 |
|---|---:|---|
| `info` | yes | `{}`；优先用 `GET /v1/info` |
| `capabilities` | yes | `{}` |
| `tab.list` | yes | `{}`；result 是完整 Info |
| `tab.new` | no | 可选 `url`，省略为 `about:blank`；新 tab 会激活 |
| `tab.activate` | no | target 是权威选择；可选 `params.tabId` 必须与 target 一致 |
| `tab.close` | no | 同上；最后一个 tab 不允许关闭 |
| `navigate` | no | 必填 `url` |
| `back`, `forward`, `reload` | no | `{}` |
| `set.viewport` | no | `width,height` 必填；实现范围均为 1..8192，且 target 必须先是 active tab |
| `done` | no | `{}`；返回 `{"released":true}`，表示 action mode 完成，不关闭浏览器 |
| `quit` | no | `{}`；返回 `{"quitting":true}`，随后宿主优雅关闭（等价于 `POST /v1/quit`） |

```json
{"action":"tab.new","params":{"url":"https://example.com"}}
{"action":"tab.activate","params":{},"target":{"browserId":"...","tabId":"...","targetId":"..."}}
{"action":"navigate","params":{"url":"https://example.com/account"},"target":{"browserId":"...","tabId":"...","targetId":"..."}}
{"action":"set.viewport","params":{"width":1280,"height":720},"target":{"browserId":"...","tabId":"...","targetId":"..."}}
{"action":"tab.close","params":{},"target":{"browserId":"...","tabId":"...","targetId":"..."}}
{"action":"quit","params":{}}
```

Host action 完成后 result 通常是更新后的 Info；应使用其中的新 tab 列表重新 pin。`meta` 表示该动作实际作用的 target：导航后 URL/title 会刷新，`tab.close` 保留被关闭 target 的原始 meta，`tab.new` 使用新建 target。动作在非 active tab 上执行并不自动切换 UI；只有显式 `tab.activate` 或 CLI `--follow` 会激活。

```sh
kitty-browse action -socket "$SOCKET" -- tab list
kitty-browse action -socket "$SOCKET" -- tab new https://example.com
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- tab activate
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- navigate https://example.com/account
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- back
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- reload
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- set viewport 1280 720
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- tab close
```

### Network、console、cookie 与 storage

| action | readOnly | params / result |
|---|---:|---|
| `console`, `errors` | yes | `clear`; 返回 `{entries:[...]}`，读取后可清空内存缓冲 |
| `network.requests` | yes | `filter` 是 URL 子串，`clear`; 返回无 headers/body 的 `{requests:[...]}` |
| `network.response` | yes | 必填已观察到的 `requestId`; body 最多 1 MiB，返回 `base64Encoded,truncated,size` |
| `download.list` | yes | 返回 browser-scoped 最近 64 条下载 metadata，不读取文件内容 |
| `download.wait` | yes | 可选 `guid`；否则等待最近 5 秒未消费下载，返回 GUID 文件路径、建议文件名和字节状态 |
| `cookies.get` | yes | 可选 `name`; 仅当前 URL 可见 cookies |
| `cookies.set` | no | 必填 `name,value`; 可选 `url,domain,path,expires,secure,httpOnly,sameSite`; URL 默认当前 URL |
| `cookies.clear` | no | 可选 `name`; 清除当前 URL 可见的匹配 cookies |
| `storage.get` | yes | 必填 `kind=local|session`; 可选 `key`; 仅当前 origin |
| `storage.set` | no | 必填 `kind,key,value`; 仅当前 origin |
| `storage.clear` | no | 必填 `kind`; 清空当前 origin 的该 storage |

```json
{"action":"console","params":{}}
{"action":"network.requests","params":{"filter":"/api/"}}
{"action":"network.response","params":{"requestId":"12345.67"}}
{"action":"cookies.get","params":{"name":"session"}}
{"action":"cookies.set","params":{"name":"theme","value":"dark","url":"https://example.com","secure":true,"sameSite":"Lax"}}
{"action":"storage.get","params":{"kind":"local","key":"theme"}}
{"action":"storage.set","params":{"kind":"session","key":"wizardStep","value":"2"}}
```

观察从 tab 初始化、首次加载前开始，只覆盖本 kitty-browse 实例管理的 session。network listing 有意不保留 Cookie、Authorization、request body 或 headers；userinfo、fragment 及 token/auth/code/key/secret/password/signature/session/cookie 类 query 值会脱敏。`network.response` 只能读取仍可由 Chromium 提供且已在当前 tab 观察到的 response body，已知超过 1 MiB 时会直接拒绝。不要把 cookie/storage/response body 输出到日志。

下载为 browser-scoped；持久 profile 默认保存到 `<profile>/Downloads`，private 默认下载目录在正常退出后删除。`-download-dir` 是显式 artifact 目录，即使 private 模式也会保留。Chrome 使用 GUID 文件名以避免相同建议文件名覆盖；API 返回 `suggestedFilename` 供调用方展示，不自动重命名。

```sh
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- console
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- errors
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- network requests --filter /api/
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- network response REQUEST_ID
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- cookies get theme
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- cookies set theme dark --secure --same-site Lax
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- storage local get theme
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- storage session set wizardStep 2
```

### Screenshot 与 PDF artifact

| action | readOnly | params | result |
|---|---:|---|---|
| `screenshot` | yes | `full`; 可选 `path` | 无 path：`{mimeType:"image/png",data:<base64>,width,height,bytes}`；有 path：`{mimeType,path,width,height,bytes}` |
| `pdf` | yes | 可选 `path` | 无 path：`{mimeType:"application/pdf",data:<base64>,bytes}`；有 path：`{mimeType,path,bytes}` |

```json
{"action":"screenshot","params":{"full":false}}
{"action":"screenshot","params":{"full":true,"path":"/private/tmp/page.png"}}
{"action":"pdf","params":{}}
{"action":"pdf","params":{"path":"/private/tmp/page.pdf"}}
```

inline artifact 字段名实际为 `result.data`，内容是 base64；path 模式为 `result.path`。截图 path 会解析为绝对路径、按需创建 `0700` parent，并以 `0600` 新建文件；PDF 以 `0600` 新建指定文件但不创建 parent。二者都使用 exclusive create，不覆盖已有文件/符号链接。PDF 执行器还有独立的 30 秒上限：更短的 request timeout 会提前取消，更长的 `timeoutMs` 不会延长 PDF 到 30 秒以上。调用方应优先 inline 并在内存中消费；只有用户明确需要且内容不敏感时才提供 path。

```sh
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- screenshot /private/tmp/page.png --full
kitty-browse action -socket "$SOCKET" -tab TAB_ID -- pdf /private/tmp/page.pdf
# 原始 PNG endpoint 的独立 CLI（只针对 active tab）：
kitty-browse screenshot -socket "$SOCKET" -out /private/tmp/active.png -full-page
```

## 8. 凭据与 CLI stdin

不要把密码、cookie、token、storage value、带秘密的 JS 或表单内容放到 CLI argv；argv 可能被进程列表、shell history 或任务日志读取。需要通过 CLI 时使用 typed raw JSON stdin：

```sh
# request.json 应来自受保护的内存管道或 0600 临时文件；不要提交到 workspace。
kitty-browse action -socket "$SOCKET" -request-json - < request.json
```

也可以直接把受保护的生成器 pipe 到 `curl --data-binary @-`。`-request-json '...secret...'` 虽受支持，但会把内容放进 argv，不适合凭据。CLI raw JSON 仍会先 `/v1/info`、解析并 pin target；`--tab`/`--target`/`--browser` 只在显式给出时覆盖 raw request target。

## 9. 明确未实现的 advanced surface

当前原生 API 不宣称与 terminal-browser 或 agent-browser 全量兼容，也没有外部 helper passthrough。当前源码没有以下 capability：

- video/record、HAR record、performance trace；
- network request routing、intercept、mock、abort 或 response rewrite；
- device/touch/mobile/CPU/network 仿真、geolocation、timezone、locale、permission 管理；
- closed shadow root 穿透；
- WebMCP、drag-and-drop、完整 Playwright locator 或逐字 agent-browser CLI 兼容层。

不要根据其他浏览器工具的同名命令猜测能力。只调用 `/v1/info` 宣告的 capability，并以返回 schema、`readOnly` 和本文件列出的实际边界为准。

### 当前 schema/执行器注意点

Capability schema 可以直接生成 golder 工具的名称、描述、参数形状和 `readOnly` 标记。服务端校验未知参数、required/type/enum/minimum/pattern；golder仍应在发送前校验以减少无效调用。

- `frame` schema只接受 `selector`/`main`；`scroll` 的元素定位只接受 `selector`/`ref`。
- `snapshot.maxNodes` 在 typed schema 中存在，当前 CLI parser 没有对应 flag；需要此参数时直接发 typed JSON。
- 通用 request schema 允许 `timeoutMs` 到 120000，但 PDF 执行器内部固定最多 30 秒。
- 前述 clear 操作以及 path artifact 是 `readOnly:true` 下的本地副作用，不应把该标记理解成“绝对无任何状态变化”。

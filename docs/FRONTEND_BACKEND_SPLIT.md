# 前后端分离部署说明

面板原本是 **go:embed 单文件前端**（index.html / app.js / echarts.min.js 打进二进制，
与 Go 服务同体部署）。改造后两种形态都支持，**默认行为不变**。

## 一、三种部署形态

| 形态 | 前端位置 | 后端 | 何时用 |
| --- | --- | --- | --- |
| A 同体部署（默认） | 编译进二进制 | 同一个 Go 进程 | 单机自用、最省事 |
| B 同源反代 | 独立静态主机/容器 | Go 只跑 API | 前端上 CDN 或 Nginx，同一域名反代 `/panel/api` |
| C 跨域调用 | 独立域名 | Go 只跑 API + CORS | 前端与后端域名不同 |

A 不需要任何配置；B/C 用下面的开关。

## 二、配置项

`config.json`：

```json
{
  "panel": {
    "cors_origins": ["https://panel.example.com", "https://*.example.org"],
    "web_dir": ""
  }
}
```

对应环境变量（优先级高于文件）：

```bash
WB2A_CORS_ORIGINS="https://panel.example.com,https://*.example.org"
WB2A_WEB_DIR="D:/workbuddy2api-panel/internal/panel"
```

- `cors_origins`：跨域来源白名单。支持精确串、`*`、`https://*.example.com` 通配子域。
  **留空 = 不启用 CORS**，行为与改造前完全一致。
- `web_dir`：前端资源目录。非空且文件存在时**按请求实时读盘**（改完刷新即生效，不用重编译）；
  空或读取失败时回退到编译进二进制的内置副本。

## 三、前端 API 地址注入

前端默认请求同源 `/panel/api/*`。跨域部署时给它一个地址即可，**无需改源码**：

```html
<script>window.WB2A_API_BASE = "https://api.example.com/panel/api";</script>
```

放在 `index.html` 的 `</head>` 之前。

## 四、本地分离开发

两个终端，各自重启互不影响：

```bash
# 终端 1：后端（只提供 API，放行前端地址）
WB2A_LISTEN=127.0.0.1:7863 \
WB2A_CORS_ORIGINS=http://127.0.0.1:5173 \
go run ./cmd/server

# 终端 2：前端独立静态服务（零依赖，自动注入 API_BASE）
node scripts/serve-web.mjs --port 5173 --api http://127.0.0.1:7863
```

打开 <http://127.0.0.1:5173/panel/>，前端改完刷新即可，GraphQL 式的"改一行等编译"不再存在。

`scripts/serve-web.mjs` 的静态根就是 `internal/panel/`，**前端源码只有一份**，不做复制，
避免双份维护漂移。

## 五、生产部署（形态 B / C）

### B 同源反代（推荐，无 CORS 复杂度）

```nginx
location /panel/api/ {
    proxy_pass http://127.0.0.1:7863;          # 保留 /panel/api 前缀
    proxy_set_header Host $host;
}
location /panel/ {
    root /srv/wb2api-web;                      # index.html / app.js / echarts.min.js
    try_files $uri $uri/ /panel/index.html;
}
```

此时 `cors_origins` 保持为空即可。

### C 跨域

```nginx
# 前端站
location / { root /srv/wb2api-web; }
# 后端站
location /panel/api/ {
    proxy_pass http://127.0.0.1:7863;
    add_header Access-Control-Allow-Origin $http_origin always;
}
```

或者在 Go 里配 `WB2A_CORS_ORIGINS`，让后端自己应答预检（已实现，返回 204）。

## 六、安全提示

- 面板鉴权是 **Bearer**（Authorization 头），不依赖 Cookie，所以 CORS 不需要
  `Allow-Credentials`；本实现已显式关闭该头，避免 `*` 组合被浏览器拒绝。
- 不要把 `cors_origins` 配成 `*` 放公网——等于允许任意站点用你的 key 调面板。
  确需放开时，至少配合 IP 白名单或反向代理层鉴权。
- `web_dir` 只读取白名单内的三个文件名，路径含 `..` 或分隔符直接拒绝，不会越界。

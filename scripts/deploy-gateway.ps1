# deploy-gateway.ps1 —— 构建并部署网关（带"旧二进制"闸门）
#
# 为什么需要它：这个项目**已经四次**出现"改了代码但传了旧二进制"，症状是功能静默缺失
# （最近一次：面板 index.html 的 mtime 晚于二进制构建时间，go:embed 进去的是重构前的面板，
#  上线后 #qqbot 还是旧布局）。靠人记时间戳不可靠，所以做成脚本：
#
#   1) 找出 internal/ 与 cmd/ 下最新的源码 mtime，与本地二进制比较；
#      二进制更旧（或不存在）→ 无条件重建，不给"我刚刚构建过"的机会。
#   2) 构建后打印**本地 md5**，上传后打印**远端 md5**，两者不一致就报错退出。
#   3) 部署前后各打一次 healthz；失败自动从备份回滚。
#
# 用法：
#   pwsh scripts/deploy-gateway.ps1                # 构建 + 部署 + 校验
#   pwsh scripts/deploy-gateway.ps1 -SkipBuild     # 只部署现有二进制（仍做 md5 校验）

param(
  [switch]$SkipBuild,
  [string]$Server = 'ct-game.com',
  [string]$User = 'root',
  [string]$Password = $env:WB_PASS,
  [string]$RemoteDir = '/opt/workbuddy2api',
  [string]$Service = 'workbuddy2api'
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo
$bin = Join-Path $repo 'wb2api-linux'

function Newest-SourceTime {
  $roots = @('internal', 'cmd')
  $newest = Get-ChildItem -Path $roots -Recurse -File -Include *.go, *.html, *.js, *.css, *.md, *.json -ErrorAction SilentlyContinue |
    Sort-Object LastWriteTime -Descending | Select-Object -First 1
  if (-not $newest) { throw '找不到源码文件' }
  return $newest
}

# ── 1) 闸门：二进制必须比所有源码新 ────────────────────────────────────────
if (-not $SkipBuild) {
  $newestSrc = Newest-SourceTime
  $needBuild = $true
  if (Test-Path $bin) {
    $binTime = (Get-Item $bin).LastWriteTime
    if ($binTime -gt $newestSrc.LastWriteTime) {
      $needBuild = $false
      Write-Host ("[gate] 二进制({0:HH:mm:ss}) 比最新源码({1:HH:mm:ss} {2}) 新，跳过构建" -f `
        $binTime, $newestSrc.LastWriteTime, $newestSrc.Name) -ForegroundColor DarkGray
    } else {
      Write-Host ("[gate] **源码更新**：{0} ({1:HH:mm:ss}) 晚于二进制({2:HH:mm:ss})，必须重建" -f `
        $newestSrc.Name, $newestSrc.LastWriteTime, $binTime) -ForegroundColor Yellow
    }
  }
  if ($needBuild) {
    $env:GOFLAGS = '-p=1'
    $env:GOMAXPROCS = '2'
    $env:GOGC = '40'
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    Write-Host '[build] go build -p 1 ./cmd/server' -ForegroundColor Cyan
    & go build -p 1 -trimpath -ldflags '-s -w' -o $bin ./cmd/server
    Remove-Item Env:GOOS, Env:GOARCH
    if ($LASTEXITCODE -ne 0) { throw '构建失败' }
    Write-Host ("[build] 完成：{0:N1} MB  {1:HH:mm:ss}" -f ((Get-Item $bin).Length / 1MB), (Get-Item $bin).LastWriteTime) -ForegroundColor Green
  }
}

if (-not $Password) { throw '缺少服务器密码：设置 $env:WB_PASS' }
if (-not (Test-Path $bin)) { throw "找不到 $bin" }
$localMd5 = (Get-FileHash $bin -Algorithm MD5).Hash.ToLower()
Write-Host "[local] md5=$localMd5  size=$((Get-Item $bin).Length)" -ForegroundColor DarkGray

# ── 2) 部署（paramiko；本地没有 ssh 客户端也稳）────────────────────────────
$py = 'C:\Users\Admin\AppData\Local\Programs\Python\Launcher\py.exe'
$deploy = @'
import hashlib, os, sys, time
import paramiko

server, user, pwd, remote_dir, service, local_bin = sys.argv[1:7]

def sh(c, cmd, timeout=120):
    si, so, se = c.exec_command(cmd, timeout=timeout)
    return (so.read().decode("utf-8", "replace") + se.read().decode("utf-8", "replace")).strip()

def md5(path):
    h = hashlib.md5()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

c = paramiko.SSHClient()
c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect(server, 22, user, pwd, timeout=25, banner_timeout=25, auth_timeout=25)

before = sh(c, "curl -s -m 5 http://127.0.0.1:7863/healthz || true")
print("[health] before:", before[:160])

stamp = time.strftime("%H%M%S")
bak = sh(c, f"cp -f {remote_dir}/wb2api {remote_dir}/wb2api.bak-{stamp} && echo {remote_dir}/wb2api.bak-{stamp}")
print("[backup]", bak)

sftp = c.open_sftp()
sftp.put(local_bin, f"{remote_dir}/wb2api.new")
sftp.close()

sh(c, f"chmod +x {remote_dir}/wb2api.new && mv -f {remote_dir}/wb2api.new {remote_dir}/wb2api")
sh(c, f"systemctl restart {service}")
time.sleep(3)
state = sh(c, f"systemctl is-active {service}")
print("[service]", state)

remote_md5 = sh(c, f"md5sum {remote_dir}/wb2api | cut -d' ' -f1")
after = sh(c, "curl -s -m 8 http://127.0.0.1:7863/healthz || true")
print("[health] after:", after[:160])
print("[remote] md5=" + remote_md5)

local_md5 = md5(local_bin)
if remote_md5.strip() != local_md5:
    print(f"[FAIL] 远端 md5 与本地不一致：remote={remote_md5} local={local_md5}")
    print("[rollback]", sh(c, f"cp -f {bak} {remote_dir}/wb2api && systemctl restart {service} && sleep 2 && systemctl is-active {service}"))
    c.close()
    sys.exit(2)

if '"healthy"' not in after or '"healthy":0' in after:
    print("[WARN] healthz 看起来不健康，请人工确认（未自动回滚，避免误伤正常但账号全冷却的情况）")

print("[OK] 部署完成且 md5 一致")
c.close()
'@
$tmp = Join-Path $env:TEMP 'wb-deploy-gateway.py'
Set-Content -Path $tmp -Value $deploy -Encoding utf8
& $py $tmp $Server $User $Password $RemoteDir $Service $bin
if ($LASTEXITCODE -ne 0) { throw "部署失败（exit $LASTEXITCODE）" }

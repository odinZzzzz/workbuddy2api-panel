# 部署脚本：上传 linux 二进制 → 重启 systemd → 自检
#
# 为什么需要它：这套服务的发布动作是「交叉编译 → scp → 覆盖 → systemctl restart →
# 验 healthz」，手工做一遍 5 分钟且容易漏掉最后一步自检（曾经因此把 404 的旧二进制
# 当成功部署）。脚本把这段固化，失败就非零退出。
#
# 凭据**不写死**：从环境变量读，缺失就报错退出。
#   $env:WB_HOST='ct-game.com'; $env:WB_USER='root'; $env:WB_PASS='...'
#   pwsh scripts/deploy.ps1
#
# 前置：本机 py.exe（Python 3）带 paramiko；或已配好免密 SSH 时用 -UseSsh。
param(
  [string]$HostName = $env:WB_HOST,
  [string]$User = $env:WB_USER,
  [string]$Password = $env:WB_PASS,
  [string]$RemoteDir = '/opt/workbuddy2api',
  [string]$Service = 'workbuddy2api',
  [int]$Port = 7863,
  [string]$ApiKey = $env:WB_API_KEY,
  [switch]$UseSsh
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $root 'wb2api-linux-amd64'

if (-not (Test-Path $bin)) { throw "找不到 $bin，先跑 pwsh scripts/build.ps1 -Target linux" }
if (-not $HostName) { throw "缺少目标主机：设置 `$env:WB_HOST" }
if (-not $User) { throw "缺少用户名：设置 `$env:WB_USER" }

$remoteTmp = "$RemoteDir/wb2api.new"

if ($UseSsh) {
  # 免密 SSH 路线（已配 key 时最省事）
  & scp -q $bin "${User}@${HostName}:$remoteTmp"
  if ($LASTEXITCODE -ne 0) { throw "scp 失败" }
  & ssh "${User}@${HostName}" "set -e; systemctl stop $Service; cp -a $RemoteDir/wb2api $RemoteDir/wb2api.bak-`$(date +%Y%m%d-%H%M%S); mv -f $remoteTmp $RemoteDir/wb2api; chmod 755 $RemoteDir/wb2api; systemctl start $Service; sleep 5; systemctl is-active $Service; curl -s -o /dev/null -w 'healthz=%{http_code}\n' http://127.0.0.1:$Port/healthz"
  if ($LASTEXITCODE -ne 0) { throw "远程重启失败" }
}
else {
  # 密码路线（paramiko；本机没有 sshpass 时唯一可行的自动化方式）
  if (-not $Password) { throw "缺少密码：设置 `$env:WB_PASS（或用 -UseSsh 走免密 key）" }
  $py = (Get-Command py.exe -ErrorAction SilentlyContinue).Source
  if (-not $py) { $py = (Get-Command python -ErrorAction SilentlyContinue).Source }
  if (-not $py) { throw "找不到 Python（需要 paramiko）" }

  $script = @'
import os, sys, time
import paramiko

host, user, pwd = sys.argv[1], sys.argv[2], sys.argv[3]
local, remote_tmp, remote_dir, service, port = sys.argv[4:9]
key = os.environ.get("WB_API_KEY", "")

c = paramiko.SSHClient()
c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect(host, 22, user, pwd, timeout=25, banner_timeout=25, auth_timeout=25)

def run(cmd, timeout=180):
    si, so, se = c.exec_command(cmd, timeout=timeout)
    out = so.read().decode("utf-8", "replace").strip()
    err = se.read().decode("utf-8", "replace").strip()
    return out or err

sftp = c.open_sftp(); sftp.put(local, remote_tmp); sftp.close()
print("上传完成:", remote_tmp)

run(f"systemctl stop {service}; cp -a {remote_dir}/wb2api {remote_dir}/wb2api.bak-$(date +%Y%m%d-%H%M%S); "
    f"mv -f {remote_tmp} {remote_dir}/wb2api; chmod 755 {remote_dir}/wb2api; systemctl start {service}")
time.sleep(5)
active = run(f"systemctl is-active {service}")
print("service:", active)
if active.strip() != "active":
    print(run(f"tail -30 /var/log/{service}/stderr.log"))
    sys.exit(2)

hz = run(f"curl -s -o /dev/null -w '%{{http_code}}' http://127.0.0.1:{port}/healthz")
print("healthz:", hz)
if hz.strip() not in ("200", "503"):   # 503 = 无可用账号，服务本身正常
    print(run(f"tail -30 /var/log/{service}/stderr.log"))
    sys.exit(3)

if key:
    print("status:", run(f"curl -s -H 'Authorization: Bearer {key}' http://127.0.0.1:{port}/status | head -c 200"))
print("最近日志:")
print(run(f"tail -8 /var/log/{service}/stderr.log"))
c.close()
'@
  $tmp = Join-Path $env:TEMP 'wb-deploy-inline.py'
  Set-Content $tmp $script -Encoding utf8
  & $py $tmp $HostName $User $Password $bin $remoteTmp $RemoteDir $Service $Port
  if ($LASTEXITCODE -ne 0) { throw "部署失败（exit $LASTEXITCODE）" }
}

"部署完成：${HostName}:${Port}"

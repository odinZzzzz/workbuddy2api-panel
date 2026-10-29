# 构建脚本：一条命令出可部署产物
#
# 为什么需要它：本机内存紧张（31GB 但常被别的程序吃掉），`go build ./...` 默认并行
# 编译会 fork 一堆 compile.exe，直接撞 "The paging file is too small"（实测过两次）。
# 于是每次都得手写 `-p 1 -trimpath -ldflags="-s -w"` —— 记错一次就白等一轮。
# 这个脚本把「低并发 + 可复现 flags + 体积/耗时反馈」固化下来。
#
# 用法：
#   pwsh scripts/build.ps1              # 只编本机（快速迭代）
#   pwsh scripts/build.ps1 -Target all  # 本机 + linux/amd64（部署用）
#   pwsh scripts/build.ps1 -Target linux
param(
  [ValidateSet('host', 'linux', 'all')]
  [string]$Target = 'host',
  # 并发度：1 = 最省内存（默认）；内存宽裕时可调到 2~4 换取更快的冷编译
  [int]$Parallel = 1,
  [string]$Out = ''
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  $env:CGO_ENABLED = '0'
  $env:GOGC = '40'          # 编译期 GC 更激进，峰值内存更低
  $env:GOMAXPROCS = "$Parallel"
  $flags = @('-trimpath', '-ldflags=-s -w')

  function Build-One([string]$goos, [string]$goarch, [string]$outFile) {
    $env:GOOS = $goos
    $env:GOARCH = $goarch
    $sw = [Diagnostics.Stopwatch]::StartNew()
    & go build -p $Parallel @flags -o $outFile ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw "go build 失败（$goos/$goarch）" }
    $sw.Stop()
    $mb = [math]::Round((Get-Item $outFile).Length / 1MB, 2)
    "  {0,-28} {1,6:N1}s  {2,6} MB" -f $outFile, $sw.Elapsed.TotalSeconds, $mb
  }

  "构建（-p $Parallel, trimpath, -s -w）"
  if ($Target -in @('host', 'all')) {
    $hostOut = if ($Out) { $Out } else { 'wb2api.exe' }
    Build-One 'windows' 'amd64' $hostOut
  }
  if ($Target -in @('host', 'linux', 'all')) {
    Build-One 'linux' 'amd64' 'wb2api-linux-amd64'
  }
  ""
  "完成。部署用：pwsh scripts/deploy.ps1"
}
finally {
  Remove-Item Env:GOOS -ErrorAction SilentlyContinue
  Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
  Pop-Location
}

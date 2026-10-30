# 安装 git 钩子：提交前自动跑密钥闸门。
#
# 为什么需要单独一个安装脚本：.git/hooks/ 不进版本库，新克隆的仓库默认没有钩子。
# 把钩子内容版本化在 scripts/ 里、由本脚本写入 .git/hooks/，才能"克隆后一条命令装好"。
#
# 用法：pwsh scripts/install-hooks.ps1 [-Force]
param([switch]$Force)
$ErrorActionPreference = 'Stop'
$repo = (& git -C (Split-Path -Parent $PSScriptRoot) rev-parse --show-toplevel)
if (-not $repo) { throw "不在 git 仓库里" }
$hooks = Join-Path $repo '.git/hooks'
New-Item -ItemType Directory -Force -Path $hooks | Out-Null

$pre = Join-Path $hooks 'pre-commit'
if ((Test-Path $pre) -and -not $Force) {
  Write-Host "⚠️  $pre 已存在（用 -Force 覆盖）"
  exit 0
}

# 用 sh 写：git for Windows 用 sh 执行钩子；pwsh 在 PATH 里即可调用。
$content = @'
#!/bin/sh
# 提交前密钥闸门：发现疑似明文 api_key/token/私钥就拒绝提交。
# 由 scripts/install-hooks.ps1 安装；判据见 scripts/check-secrets.ps1。
ROOT="$(git rev-parse --show-toplevel)"
if command -v pwsh >/dev/null 2>&1; then
  pwsh -NoProfile -File "$ROOT/scripts/check-secrets.ps1" -Staged || {
    echo ""
    echo "提交被拒绝：暂存区里有疑似明文密钥。"
    exit 1
  }
else
  echo "warn: 未找到 pwsh，跳过密钥闸门"
fi
exit 0
'@
# 钩子文件必须是 LF 且可执行
[IO.File]::WriteAllText($pre, ($content -replace "`r`n", "`n"))
Write-Host "✅ 已安装 pre-commit 钩子：$pre"
Write-Host "   验证：git commit 时自动扫描暂存区；误报可临时 git commit --no-verify"

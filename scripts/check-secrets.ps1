# 密钥泄漏闸门：扫描入库文件里疑似真实凭据的明文。
#
# 为什么需要：这个仓库会把脚本、文档一起入库，而调试时最容易顺手把 api_key 写进
# 脚本常量或示例。一旦进了 git 历史，删文件也删不掉（要 rewrite history），成本极高。
# 所以把检查前置到提交前：发现问题就拒绝提交。
#
# 用法：
#   pwsh scripts/check-secrets.ps1            # 扫已跟踪文件（默认）
#   pwsh scripts/check-secrets.ps1 -Staged    # 只扫暂存区（pre-commit 用）
#   pwsh scripts/check-secrets.ps1 -All       # 扫整个工作树（含未跟踪，排除 .git）
#
# 退出码：0 = 干净；1 = 发现疑似密钥。
param(
  [switch]$Staged,
  [switch]$All
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot

# 判据：真实凭据的形态。刻意不匹配 "sk-..." / "sk-xxx" 这类占位符与文档示例。
$patterns = @(
  @{ name = '网关 api_key'; re = 'sk-[0-9a-f]{32,}' },
  @{ name = 'JWT';          re = 'eyJ[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\.' },
  @{ name = '私钥';          re = '-----BEGIN [A-Z ]*PRIVATE KEY-----' },
  @{ name = '长随机串赋值';  re = '(?i)(api[_-]?key|token|secret)\s*[:=]\s*["''](?!\s*["''])[A-Za-z0-9_\-]{32,}["'']' }
)

# 白名单：示例/文档里允许出现占位符的路径
$allowPaths = @(
  'config.example.json',
  'scripts/check-secrets.ps1',
  'README.md'
)

function Get-TargetFiles {
  if ($All) {
    # -All 扫工作树，但跳过**被 git 忽略**的文件：config.json / data/ / auths/ 这类
    # 按设计就要放明文密钥且永不入库，扫它们只会天天误报（真正要防的是"误提交"）。
    $ignored = @(& git -C $repo ls-files --others --ignored --exclude-standard)
    Get-ChildItem -Path $repo -Recurse -File -ErrorAction SilentlyContinue |
      Where-Object { $_.FullName -notmatch '\\.git\\|node_modules|\\dist\\' } |
      ForEach-Object { $_.FullName.Substring($repo.Length + 1).Replace('\', '/') } |
      Where-Object { $ignored -notcontains $_ }
  }
  elseif ($Staged) {
    (& git -C $repo diff --cached --name-only --diff-filter=ACM) | Where-Object { $_ }
  }
  else {
    (& git -C $repo ls-files) | Where-Object { $_ }
  }
}

$files = Get-TargetFiles
$findings = @()
foreach ($f in $files) {
  if ($allowPaths -contains $f) { continue }
  if ($f -match '\.(png|jpg|jpeg|gif|webp|ico|zip|gz|exe|bin)$') { continue }
  $full = Join-Path $repo $f
  if (-not (Test-Path -LiteralPath $full)) { continue }
  $content = Get-Content -LiteralPath $full -Raw -ErrorAction SilentlyContinue
  if (-not $content) { continue }
  foreach ($p in $patterns) {
    foreach ($m in [regex]::Matches($content, $p.re)) {
      $val = $m.Value
      $masked = if ($val.Length -gt 14) { $val.Substring(0, 8) + '…' + $val.Substring($val.Length - 4) } else { '***' }
      $lineNo = ($content.Substring(0, $m.Index) -split "`n").Count
      $findings += "  $f`:$lineNo  疑似 $($p.name)：$masked"
    }
  }
}

if ($findings.Count -gt 0) {
  Write-Host "❌ 发现疑似明文密钥（禁止提交）：" -ForegroundColor Red
  $findings | Sort-Object -Unique | ForEach-Object { Write-Host $_ }
  Write-Host ""
  Write-Host "处置建议：" -ForegroundColor Yellow
  Write-Host "  1. 脚本里改为从环境变量或 ~/.dsh/.credentials.yaml 读取；"
  Write-Host "  2. 配置类文件加入 .gitignore（config.json / *.env / auths/ 已覆盖）；"
  Write-Host "  3. 若已进入 git 历史，需 rewrite history 并轮换该密钥。"
  exit 1
}

Write-Host "✅ 未发现明文密钥（扫描 $($files.Count) 个文件）"
exit 0

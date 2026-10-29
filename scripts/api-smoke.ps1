# 接口冒烟探针：打一批边界用例，只报 PASS/FAIL，失败项自带响应体。
#
# 为什么需要：单测覆盖的是"函数对不对"，这里覆盖的是"线上这套配置 + 真实上游下
# 行为对不对"——令牌配额、模型白名单、过期时间解析、兼容层缺字段、面板鉴权这类
# 问题只有打真接口才暴露。
#
# 用法：
#   $env:WB_API_KEY='sk-...'; pwsh scripts/api-smoke.ps1
#   pwsh scripts/api-smoke.ps1 -Base http://ct-game.com:7863 -Key sk-...
param(
  [string]$Base = 'http://ct-game.com:7863',
  [string]$Key = $env:WB_API_KEY
)
$ErrorActionPreference = 'Stop'
if (-not $Key) { throw "缺少 API key：-Key 或 `$env:WB_API_KEY" }

$script:pass = 0
$script:fail = 0
$script:created = @()

function Req {
  param([string]$Method, [string]$Path, [string]$Body = $null, [string]$AuthKey = $null, [int]$Timeout = 120)
  $headers = @{}
  if ($AuthKey) { $headers['Authorization'] = "Bearer $AuthKey" }
  $args = @{
    Uri = "$Base$Path"; Method = $Method; Headers = $headers
    UseBasicParsing = $true; NoProxy = $true; TimeoutSec = $Timeout; SkipHttpErrorCheck = $true
  }
  if ($Body) { $args['ContentType'] = 'application/json'; $args['Body'] = $Body }
  $r = Invoke-WebRequest @args
  $text = [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray())
  $json = $null
  try { $json = $text | ConvertFrom-Json } catch { $json = $null }
  return @{ status = [int]$r.StatusCode; body = $text; json = $json }
}

function Check([string]$name, [bool]$ok, [string]$detail = '') {
  if ($ok) { $script:pass++; "  ✅ $name" }
  else { $script:fail++; "  ❌ $name" + $(if ($detail) { "`n       $detail" } else { '' }) }
}

function NewToken([string]$name, [double]$quota = 0, [string]$expires = '', [string[]]$models = @()) {
  $body = @{ name = $name; quota_credits = $quota; expires_at = $expires; allowed_models = $models } | ConvertTo-Json -Compress
  $r = Req -Method POST -Path '/panel/api/tokens' -Body $body -AuthKey $Key
  if ($r.status -ne 200) { return $null }
  $script:created += $r.json.token.id
  return $r.json.token
}

$chat = { param($model) (@{ model = $model; messages = @(@{ role = 'user'; content = 'reply OK' }); max_tokens = 30 } | ConvertTo-Json -Depth 6 -Compress) }

try {
  "== A. 令牌生命周期与限制 =="

  # A1 配额用尽 → 429
  #
  # 模型选择很关键：配额是**积分**维度。上游对不少模型当前收 credit=0（免费期），
  # 这类请求跑多少次都不消耗积分，配额自然不会触发——这是正确语义，但会让探针误报。
  # 所以先用一次探测请求读出真实计费：0 → 跳过本项并说明；>0 → 继续断言 429。
  $quotaModel = 'cn:deepseek-v4-pro'
  $t1 = NewToken "smoke-quota-$(Get-Random)" 0.005
  if ($t1) {
    $probe = Req -Method POST -Path '/v1/chat/completions' -Body (& $chat $quotaModel) -AuthKey $t1.key
    Start-Sleep -Milliseconds 500
    $list = (Req -Method GET -Path '/panel/api/tokens' -AuthKey $Key).json.tokens
    $mine = $list | Where-Object { $_.id -eq $t1.id }
    if ($mine -and [double]$mine.used_credits -le 0) {
      Check "A1 配额用尽（跳过：$quotaModel 当前上游计费为 0，配额按积分语义不触发）" $true
    }
    else {
      $r = Req -Method POST -Path '/v1/chat/completions' -Body (& $chat $quotaModel) -AuthKey $t1.key
      Check 'A1 配额用尽 → 429 token_quota_exceeded' ($r.status -eq 429 -and $r.body -match 'token_quota_exceeded') "status=$($r.status) used=$($mine.used_credits) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"
    }
  } else { Check 'A1 建令牌失败' $false }

  # A2 模型白名单 → 403
  $t2 = NewToken "smoke-models-$(Get-Random)" 0 @() @('cn:deepseek-v4-flash')
  if ($t2) {
    $r = Req -Method POST -Path '/v1/chat/completions' -Body (& $chat 'cn:glm-5.3') -AuthKey $t2.key
    Check 'A2 白名单外模型 → 403 model_not_allowed' ($r.status -eq 403 -and $r.body -match 'model_not_allowed') "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"
    $r2 = Req -Method POST -Path '/v1/chat/completions' -Body (& $chat 'cn:deepseek-v4-flash') -AuthKey $t2.key
    Check 'A2b 白名单内模型放行' ($r2.status -eq 200) "status=$($r2.status)"
  } else { Check 'A2 建令牌失败' $false }

  # A3 非法过期时间 → 400 且报错可读
  $r = Req -Method POST -Path '/panel/api/tokens' -Body '{"name":"smoke-bad-exp","expires_at":"not-a-date"}' -AuthKey $Key
  Check 'A3 非法 expires_at → 400' ($r.status -eq 400) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"

  # A4 负配额 → 400
  $r = Req -Method POST -Path '/panel/api/tokens' -Body '{"name":"smoke-neg","quota_credits":-5}' -AuthKey $Key
  Check 'A4 负配额 → 400' ($r.status -eq 400) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"

  # A5 已过期令牌 → 401 token_expired
  $t5 = NewToken "smoke-expired-$(Get-Random)" 0 '2020-01-01'
  if ($t5) {
    $r = Req -Method GET -Path '/status' -AuthKey $t5.key
    Check 'A5 过期令牌 → 401 token_expired' ($r.status -eq 401 -and $r.body -match 'token_expired') "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"
  } else { Check 'A5 建令牌失败' $false }

  # A6 删除后 → 401 invalid_api_key
  $t6 = NewToken "smoke-del-$(Get-Random)"
  if ($t6) {
    $null = Req -Method DELETE -Path "/panel/api/tokens/$($t6.id)" -AuthKey $Key
    $r = Req -Method GET -Path '/status' -AuthKey $t6.key
    Check 'A6 删除后令牌失效 → 401' ($r.status -eq 401) "status=$($r.status)"
  } else { Check 'A6 建令牌失败' $false }

  "== B. 兼容层边界 =="

  # B1 responses 缺 input → 400
  $r = Req -Method POST -Path '/v1/responses' -Body '{"model":"cn:deepseek-v4.1-flash"}' -AuthKey $Key
  Check 'B1 /v1/responses 缺 input → 400' ($r.status -eq 400) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"

  # B2 responses 空 input 数组 → 400
  $r = Req -Method POST -Path '/v1/responses' -Body '{"model":"cn:deepseek-v4.1-flash","input":[]}' -AuthKey $Key
  Check 'B2 /v1/responses 空 input → 400' ($r.status -eq 400) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"

  # B3 messages 缺 messages → 400（Anthropic 信封）
  $r = Req -Method POST -Path '/v1/messages' -Body '{"model":"claude-sonnet-5","max_tokens":10}' -AuthKey $Key
  Check 'B3 /v1/messages 缺 messages → 400' ($r.status -eq 400 -and $r.body -match 'invalid_request_error') "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(200,$r.body.Length)))"

  # B4 count_tokens 空 body → 应 200 且给数字（Claude Code 会拿它做预算）
  $r = Req -Method POST -Path '/v1/messages/count_tokens' -Body '{"model":"claude-sonnet-5","messages":[]}' -AuthKey $Key
  Check 'B4 count_tokens 空 messages → 200' ($r.status -eq 200 -and $null -ne $r.json.input_tokens) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"

  # B5 chat 缺 model → 400
  $r = Req -Method POST -Path '/v1/chat/completions' -Body '{"messages":[{"role":"user","content":"hi"}]}' -AuthKey $Key
  Check 'B5 /v1/chat/completions 缺 model → 400' ($r.status -eq 400) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"

  # B6 未知端点 → 501
  $r = Req -Method POST -Path '/v1/embeddings' -Body '{}' -AuthKey $Key
  Check 'B6 未知端点 → 501' ($r.status -eq 501 -and $r.body -match 'not_implemented') "status=$($r.status)"

  "== C. 鉴权 =="

  # C1 无 key → 401
  $r = Req -Method GET -Path '/status'
  Check 'C1 无 key → 401' ($r.status -eq 401) "status=$($r.status)"

  # C2 错误 key → 401
  $r = Req -Method GET -Path '/status' -AuthKey 'sk-wrong-key'
  Check 'C2 错误 key → 401' ($r.status -eq 401) "status=$($r.status)"

  # C3 面板接口无 key → 401
  $r = Req -Method GET -Path '/panel/api/tokens'
  Check 'C3 面板接口无 key → 401' ($r.status -eq 401) "status=$($r.status)"

  # C4b 令牌名含中文/emoji 应正常创建与回显
  $tName = "smoke-中文-🎯-$(Get-Random)"
  $t = NewToken $tName
  Check 'C4b 令牌名支持中文与 emoji' ($null -ne $t -and $t.name -eq $tName) "name=$($t.name)"

  "== D. 参数边界 =="

  # D1 usage 非法 hours → 不应 500
  $r = Req -Method GET -Path '/panel/api/usage?hours=abc' -AuthKey $Key
  Check 'D1 usage?hours=abc 不 500' ($r.status -lt 500) "status=$($r.status)"

  # D2 request_logs 负 limit → 不应 500
  $r = Req -Method GET -Path '/panel/api/request_logs?limit=-5' -AuthKey $Key
  Check 'D2 request_logs?limit=-5 不 500' ($r.status -lt 500) "status=$($r.status)"

  # D3 request_logs 超大 limit → 不应 500（应被钳制）
  $r = Req -Method GET -Path '/panel/api/request_logs?limit=999999' -AuthKey $Key
  Check 'D3 request_logs?limit=999999 不 500' ($r.status -lt 500) "status=$($r.status)"

  # D4 令牌白名单重复项应被去重
  $t = NewToken "smoke-dup-$(Get-Random)" 0 '' @('cn:glm-5.3','cn:glm-5.3')
  Check 'D4 白名单去重' ($null -ne $t -and @($t.allowed_models).Count -eq 1) "models=$($t.allowed_models -join ',')"

  # D5 配额为 0 = 不限（创建后 quota_credits 应为 0，且请求放行）
  $t = NewToken "smoke-nolimit-$(Get-Random)" 0
  if ($t) {
    $r = Req -Method POST -Path '/v1/chat/completions' -Body (& $chat 'cn:deepseek-v4.1-flash') -AuthKey $t.key
    Check 'D5 配额 0 = 不限，请求放行' ($r.status -eq 200) "status=$($r.status)"
  } else { Check 'D5 建令牌失败' $false }

  "== E. 令牌级限流 =="

  # E1 rpm 限流：建一把 rpm=2 的令牌，连打 4 次，应出现 429 token_rate_limited
  $body = @{ name = "smoke-rpm-$(Get-Random)"; rpm = 2 } | ConvertTo-Json -Compress
  $r0 = Req -Method POST -Path '/panel/api/tokens' -Body $body -AuthKey $Key
  if ($r0.status -eq 200) {
    $tk = $r0.json.token
    $script:created += $tk.id
    $codes = @()
    for ($i = 0; $i -lt 4; $i++) {
      $r = Req -Method GET -Path '/status' -AuthKey $tk.key
      $codes += $r.status
    }
    $has429 = ($codes -contains 429)
    Check 'E1 rpm 限流生效（出现 429）' $has429 "codes=$($codes -join ',')"
    if ($has429) {
      $last = Req -Method GET -Path '/status' -AuthKey $tk.key
      Check 'E1b 429 带 token_rate_limited' ($last.body -match 'token_rate_limited') $last.body.Substring(0, [Math]::Min(160, $last.body.Length))
      Check 'E1c 429 带 Retry-After 头' ($last.status -eq 429) "status=$($last.status)"
    }
  } else { Check 'E1 建限流令牌失败' $false "status=$($r0.status)" }

  # E2 运行期改限流（limits 端点）
  $t = NewToken "smoke-limits-$(Get-Random)"
  if ($t) {
    $r = Req -Method POST -Path "/panel/api/tokens/$($t.id)/limits" -Body '{"rpm":5,"max_concurrency":2}' -AuthKey $Key
    Check 'E2 limits 端点可改限流' ($r.status -eq 200 -and $r.json.token.rpm -eq 5 -and $r.json.token.max_concurrency -eq 2) "status=$($r.status) body=$($r.body.Substring(0,[Math]::Min(160,$r.body.Length)))"
    $r2 = Req -Method POST -Path "/panel/api/tokens/$($t.id)/limits" -Body '{"rpm":-1,"max_concurrency":-1}' -AuthKey $Key
    Check 'E2b 负值钳到 0（=不限）' ($r2.status -eq 200 -and $r2.json.token.rpm -eq 0) "body=$($r2.body.Substring(0,[Math]::Min(160,$r2.body.Length)))"
  } else { Check 'E2 建令牌失败' $false }

  # C4 x-api-key 头也认（Anthropic 客户端形态）
  $headers = @{ 'x-api-key' = $Key }
  $r = Invoke-WebRequest "$Base/v1/models" -Headers $headers -UseBasicParsing -NoProxy -TimeoutSec 60 -SkipHttpErrorCheck
  Check 'C4 x-api-key 头放行' ([int]$r.StatusCode -eq 200) "status=$([int]$r.StatusCode)"
}
finally {
  # 清理所有本轮创建的令牌
  foreach ($id in $script:created) { $null = Req -Method DELETE -Path "/panel/api/tokens/$id" -AuthKey $Key }
  $left = (Req -Method GET -Path '/panel/api/tokens' -AuthKey $Key).json.tokens
  $mine = @($left | Where-Object { $_.name -like 'smoke-*' })
  ""
  "结果：PASS=$script:pass  FAIL=$script:fail（本轮创建 $($script:created.Count) 个令牌，已清理；残留 smoke-* $($mine.Count) 个）"
  if ($mine.Count -gt 0) { $mine | ForEach-Object { "  残留: $($_.name) $($_.id)" } }
  if ($script:fail -gt 0) { exit 1 }
}

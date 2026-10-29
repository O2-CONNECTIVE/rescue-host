$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/app-startup-observation.ps1"
$directory = Join-Path ([IO.Path]::GetTempPath()) ("rescue-startup-contract-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $directory -ErrorAction Stop | Out-Null
try {
    $budget = [Diagnostics.Stopwatch]::new()
    if (Wait-AppStartupTrace $directory $budget) { throw 'MISSING_TRACE_ACCEPTED' }
    $budget.Stop()
    if (-not (Test-Path (Join-Path $directory 'app-startup.finish'))) { throw 'CUTOFF_MISSING' }
    if ($budget.ElapsedMilliseconds -lt 1000) { throw 'WAIT_BUDGET_NOT_EXERCISED' }
    [IO.File]::WriteAllText((Join-Path $directory 'app-startup-stages.json'), '{}')
    if (-not (Wait-AppStartupTrace $directory $budget)) { throw 'CLOSED_FILE_NOT_OBSERVED' }
    if (Wait-WryStartupTrace $directory $budget) { throw 'MISSING_WRY_TRACE_ACCEPTED' }
    [IO.File]::WriteAllText((Join-Path $directory 'wry-startup-stages.json'), '{}')
    if (-not (Wait-WryStartupTrace $directory $budget)) { throw 'CLOSED_WRY_FILE_NOT_OBSERVED' }
    $budget.Stop()
    # 前のwaitがbudgetを使い切っても、追加待機せず一度だけ完成fileを確認する。
    if ($budget.ElapsedMilliseconds -gt 3000) { throw 'SHARED_WAIT_BUDGET_OVERRUN' }
    Write-Output 'APP_STARTUP_FINITE_COLLECTION_CONTRACT_PASSED'
} finally { Remove-Item -LiteralPath $directory -Recurse -Force }

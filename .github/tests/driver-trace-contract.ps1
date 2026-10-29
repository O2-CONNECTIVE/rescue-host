$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/../../ui/tauri/native-smoke/app-startup-observation.ps1"
$directory = Join-Path ([IO.Path]::GetTempPath()) ('rescue-driver-trace-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $directory -ErrorAction Stop | Out-Null
try {
    $budget = [Diagnostics.Stopwatch]::new()
    if (Wait-DriverStageTrace $directory $budget) { throw 'MISSING_DRIVER_TRACE_ACCEPTED' }
    if (-not (Test-Path (Join-Path $directory 'driver-stage.finish'))) { throw 'DRIVER_CUTOFF_MISSING' }
    [IO.File]::WriteAllText((Join-Path $directory 'driver-stage.jsonl'), '{}')
    if (Wait-DriverStageTrace $directory $budget) { throw 'LATE_DRIVER_FILE_ACCEPTED' }
    $fresh = [Diagnostics.Stopwatch]::new()
    if (-not (Wait-DriverStageTrace $directory $fresh)) { throw 'CLOSED_DRIVER_FILE_NOT_OBSERVED' }
    Remove-Item -LiteralPath (Join-Path $directory 'driver-stage.finish')
    New-Item -ItemType Directory (Join-Path $directory 'driver-stage.finish') -ErrorAction Stop | Out-Null
    if (Wait-DriverStageTrace $directory $budget) { throw 'MARKER_WRITE_FAILURE_ACCEPTED' }
    if ($budget.ElapsedMilliseconds -gt 3000) { throw 'SHARED_WAIT_BUDGET_OVERRUN' }
    Write-Output 'DRIVER_STAGE_FINITE_COLLECTION_CONTRACT_PASSED'
} finally { Remove-Item -LiteralPath $directory -Recurse -Force }

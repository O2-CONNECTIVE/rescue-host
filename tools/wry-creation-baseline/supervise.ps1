param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$OutputDirectory,
    [Parameter(Mandatory)][string]$ApplicationPath, [Parameter(Mandatory)][string]$BuildManifest,
    [Parameter(Mandatory)][ValidateSet('with-html', 'load-html', 'blank-script')][string]$Mode,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')][string]$RunId)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$native = (Resolve-Path "$PSScriptRoot/../../ui/tauri/native-smoke").Path
. "$native/processes.ps1"
. "$native/close-observation.ps1"
. "$native/startup-observation.ps1"
. "$native/app-startup-observation.ps1"
New-Item -ItemType Directory $OutputDirectory -ErrorAction Stop | Out-Null
$appPath = (Resolve-Path $ApplicationPath).Path
$edgePath = (Resolve-Path "$ToolsDirectory/edge/msedgedriver.exe").Path
$tauriPath = (Resolve-Path "$ToolsDirectory/tauri/bin/tauri-driver.exe").Path
$manifest = Get-Content $BuildManifest -Raw | ConvertFrom-Json
$hash = (Get-FileHash $appPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($manifest.schema -ne 1 -or $manifest.kind -ne 'direct-wry-controller-contrast' -or $manifest.distribution -ne $false -or $manifest.featureTreeMatched -ne $true -or $manifest.sourceCommit -cnotmatch '^[0-9a-f]{40}$' -or $manifest.exeSha256 -cne $hash) { throw 'NATIVE_BASELINE_BUILD_REJECTED' }
$report = [ordered]@{ schema = 1; kind = 'direct-wry-controller-contrast'; productNativePass = $false
    sourceCommit = $manifest.sourceCommit; exeSha256 = $hash; fixedInputsVerified = $true; mode = $Mode; runId = $RunId
    sessionSucceeded = $false; normalCloseVerified = $false; ownerSetRecorded = $false; observedChildren = 0
    forcedApp = $false; forcedChildren = 0; cleanupStarted = $false; navigationWorkerMarkerObserved = $false }
$driverProcess = $null; $nodeProcess = $null; $appProcess = $null; $app = $null
$driver = @(); $edge = @(); $children = @(); $handles = @(); $budget = [Diagnostics.Stopwatch]::new()
$previous = @{}; $envNames = @('RESCUE_DRIVER_TRACE', 'RESCUE_HOST_STARTUP_TRACE_DIR', 'RESCUE_BASELINE_PROFILE', 'RESCUE_BASELINE_MODE', 'NATIVE_OUTPUT_DIR', 'NATIVE_APP_EXE', 'NATIVE_PROFILE', 'NATIVE_TOOLS_DIR')
foreach ($name in $envNames) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$trace = Join-Path $ToolsDirectory 'baseline-trace'
$profile = Join-Path $ToolsDirectory 'baseline-profile'
try {
    if ((Test-Path $profile) -or (Test-Path $trace)) { throw 'NATIVE_BASELINE_RESOURCE_ALREADY_EXISTS' }
    New-Item -ItemType Directory $trace -ErrorAction Stop | Out-Null
    if (@(Get-Snapshot | Where-Object ExecutablePath -eq $appPath).Count -ne 0) { throw 'NATIVE_PREEXISTING_APP_REJECTED' }
    if (@(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445)).Count -ne 0) { throw 'NATIVE_PORT_ALREADY_IN_USE' }
    $stage = Get-Content "$ToolsDirectory/driver-provenance.json" -Raw | ConvertFrom-Json
    if ($stage.variant -ne 'tauri-driver-2.1.0-stage-2' -or (Get-FileHash $tauriPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne $stage.binarySha256) { throw 'NATIVE_DIAGNOSTIC_BINARY_DRIFT' }
    $env:RESCUE_DRIVER_TRACE = Join-Path $ToolsDirectory 'driver-stage.jsonl'
    $env:RESCUE_HOST_STARTUP_TRACE_DIR = $trace
    $env:RESCUE_BASELINE_PROFILE = $profile
    $env:RESCUE_BASELINE_MODE = $Mode
    $driverProcess = Start-Process $tauriPath -ArgumentList @('--port', '4444', '--native-port', '4445', '--native-driver', "`"$edgePath`"") -PassThru -RedirectStandardOutput "$ToolsDirectory/driver.stdout.log" -RedirectStandardError "$ToolsDirectory/driver.stderr.log"
    $ready = $false; $watch = [Diagnostics.Stopwatch]::StartNew()
    do {
        if ($driverProcess.HasExited) { throw 'NATIVE_DRIVER_START_FAILED' }
        try { $ready = (Invoke-RestMethod 'http://127.0.0.1:4444/status' -TimeoutSec 2).value.ready } catch { Start-Sleep -Milliseconds 200 }
    } while (-not $ready -and $watch.ElapsedMilliseconds -lt 15000)
    if (-not $ready) { throw 'NATIVE_DRIVER_READY_TIMEOUT' }
    $snapshot = Get-Snapshot
    $driver = @($snapshot | Where-Object ProcessId -eq $driverProcess.Id)
    $edge = @($snapshot | Where-Object { $_.ParentProcessId -eq $driverProcess.Id -and $_.ExecutablePath -eq $edgePath })
    if ($driver.Count -ne 1 -or $edge.Count -ne 1) { throw 'NATIVE_DRIVER_OWNERSHIP_AMBIGUOUS' }
    Assert-ListenerSnapshot @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445)) $driver[0].ProcessId $edge[0].ProcessId
    $env:NATIVE_OUTPUT_DIR = $OutputDirectory; $env:NATIVE_APP_EXE = $appPath
    $env:NATIVE_PROFILE = $profile; $env:NATIVE_TOOLS_DIR = $ToolsDirectory
    $nodeProcess = Start-Process (Get-Command node.exe).Source -ArgumentList "`"$native/run-wry-baseline.ts`"" -PassThru -NoNewWindow -RedirectStandardOutput "$ToolsDirectory/test.stdout.log" -RedirectStandardError "$ToolsDirectory/test.stderr.log"
    if (-not $nodeProcess.WaitForExit(40000)) { throw 'NATIVE_TEST_PROCESS_TIMEOUT' }
    $session = Get-Content "$OutputDirectory/baseline-session.json" -Raw | ConvertFrom-Json
    $report.sessionSucceeded = $session.sessionCreated -eq $true
    $report.sessionTimedOut = $session.sessionOutcome -eq 'timeout'
    $report.documentChecked = $session.documentChecked -eq $true
    if ($nodeProcess.ExitCode -ne 0) { throw 'NATIVE_BASELINE_SESSION_FAILED' }
    if ($session.browserVersion -ne (Get-Content "$native/tools.json" -Raw | ConvertFrom-Json).runtimeVersion) { throw 'NATIVE_ACTUAL_RUNTIME_MISMATCH' }
    if ($session.kind -ne 'direct-wry-controller-contrast' -or $session.productNativePass -ne $false -or $session.sessionCreated -ne $true -or $session.documentChecked -ne $true) { throw 'NATIVE_BASELINE_SESSION_REJECTED' }
    $report.sessionSucceeded = $true
    $owned = Get-OwnedApp $driver[0] $edge[0] $appPath (Get-Snapshot)
    $app = $owned.App; $children = @($owned.Children); $handles = @(Open-ChildObservationHandles $children)
    $report.observedChildren = $children.Count
    if ($children.Count -gt 32 -or $handles.Count -ne $children.Count -or @($handles | Where-Object Identity -ne 'verified').Count -gt 0) { throw 'NATIVE_BASELINE_OWNER_UNCONFIRMED' }
    $report.ownerSetRecorded = $true
    $report.wryStartupFileClosed = Wait-WryStartupTrace $trace $budget
    $appProcess = Get-Process -Id $app.ProcessId -ErrorAction Stop
    $live = Get-Snapshot; Require-Live $driver[0] $live; Require-Live $edge[0] $live
    Request-NormalClose $app $appProcess 10000
    $childWait = [Diagnostics.Stopwatch]::StartNew()
    do {
        $remaining = @($handles | Where-Object { -not $_.Process.HasExited })
        if ($remaining.Count -eq 0) { break }
        $left = 10000 - $childWait.ElapsedMilliseconds
        if ($left -le 0) { break }
        Start-Sleep -Milliseconds ([Math]::Min(200, $left))
    } while ($childWait.ElapsedMilliseconds -lt 10000)
    $report.childWaitElapsedMs = $childWait.ElapsedMilliseconds
    # 期限後の再観測で成功へ変えない。最後の期限内観測と時計で判断する。
    if ($remaining.Count -gt 0 -or $report.childWaitElapsedMs -ge 10000) { throw 'NATIVE_OBSERVED_CHILD_CLOSE_TIMEOUT' }
    $live = Get-Snapshot; Require-Live $driver[0] $live; Require-Live $edge[0] $live
    $report.driverStableThroughClose = $true
    $report.normalCloseVerified = $true
} catch {
    $report.failureCode = if ($_.Exception.Message -match '^NATIVE_[A-Z_]+$') { $_.Exception.Message } else { 'NATIVE_BASELINE_HARNESS_FAILED' }
} finally {
    # 失敗時も所有集合を先に確定する。不確定ならkill対象へ昇格しない。
    if (-not $report.ownerSetRecorded -and $handles.Count -eq 0 -and $driver.Count -eq 1 -and $edge.Count -eq 1) {
        try {
            $owned = Get-OwnedApp $driver[0] $edge[0] $appPath (Get-Snapshot)
            $app = $owned.App; $children = @($owned.Children); $handles = @(Open-ChildObservationHandles $children)
            $report.observedChildren = $children.Count
            $report.ownerSetRecorded = $children.Count -le 32 -and $handles.Count -eq $children.Count -and @($handles | Where-Object Identity -ne 'verified').Count -eq 0
        } catch { $report.ownerSetRecorded = $false }
    }
    try { if (-not $report.Contains('wryStartupFileClosed')) { $report.wryStartupFileClosed = Wait-WryStartupTrace $trace $budget } } catch { $report.wryStartupFileClosed = $false }
    # 既存traceを先に回収し、追加診断は残予算だけを使う。
    try { $report.driverStageFileClosed = Wait-DriverStageTrace $ToolsDirectory $budget } catch { $report.driverStageFileClosed = $false }
    # markerの作成箇所への到達を確認する。threadのjoinとは区別する。
    try {
        if (-not $budget.IsRunning) { $budget.Start() }
        while ($budget.ElapsedMilliseconds -lt 1000) {
            if (Test-Path (Join-Path $trace 'navigation-worker.closed') -PathType Leaf) { $report.navigationWorkerMarkerObserved = $true; break }
            Start-Sleep -Milliseconds 10
        }
    } catch { $report.navigationWorkerMarkerObserved = $false }
    finally { $budget.Stop() }
    $report.cleanupStarted = $true; $forceErrors = @(); $forceWait = [Diagnostics.Stopwatch]::StartNew()
    try {
        if ($report.ownerSetRecorded) {
            if ($app -and -not $appProcess) { $appProcess = Get-Process -Id $app.ProcessId -ErrorAction Stop }
            if ($appProcess -and -not $appProcess.HasExited) {
                Assert-CloseTarget $app $appProcess
                $appProcess.Kill(); $report.forcedApp = $true
                if (-not $appProcess.WaitForExit([Math]::Max(0, 5000 - $forceWait.ElapsedMilliseconds))) { $forceErrors += 'app-exit-unconfirmed' }
            }
            foreach ($handle in $handles) {
                if ($handle.Identity -eq 'verified' -and -not $handle.Process.HasExited) {
                    $handle.Process.Kill(); $report.forcedChildren += 1
                    if (-not $handle.Process.WaitForExit([Math]::Max(0, 5000 - $forceWait.ElapsedMilliseconds))) { $forceErrors += 'child-exit-unconfirmed' }
                }
            }
        }
        $report.observedChildrenExited = $report.ownerSetRecorded -and @($handles | Where-Object { -not $_.Process.HasExited }).Count -eq 0
        $report.appExited = $null -ne $appProcess -and $appProcess.HasExited
    } catch { $forceErrors += 'fixture-cleanup-unconfirmed'; $report.observedChildrenExited = $false; $report.appExited = $false }
    try { Complete-NativeCleanup $nodeProcess $driverProcess $children $app $handles $report $OutputDirectory }
    finally {
        $report.cleanupFailures += $forceErrors.Count; $report.cleanupCodes += $forceErrors
        try {
            $after = Get-Snapshot
            $report.driversExited = $driver.Count -eq 1 -and $edge.Count -eq 1 -and (Get-StartupIdentity $driver[0] $after) -eq 'absent' -and (Get-StartupIdentity $edge[0] $after) -eq 'absent'
            $report.listenersAbsent = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445)).Count -eq 0
        } catch { $report.driversExited = $false; $report.listenersAbsent = $false }
        foreach ($name in $envNames) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
        if ($appProcess) { $appProcess.Dispose() }
        Save-NativeReport $report $OutputDirectory
    }
}
if (-not $report.normalCloseVerified -or $report.cleanupFailures -gt 0 -or -not $report.observedChildrenExited -or -not $report.appExited -or -not $report.driversExited -or -not $report.listenersAbsent) { exit 1 }

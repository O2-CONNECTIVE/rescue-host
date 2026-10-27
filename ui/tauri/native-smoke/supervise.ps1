param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/processes.ps1"
New-Item -ItemType Directory -Path $OutputDirectory -ErrorAction Stop | Out-Null
$appPath = (Resolve-Path "$PSScriptRoot/../src-tauri/target/debug/rescue-host-shell.exe").Path
$edgePath = (Resolve-Path "$ToolsDirectory/edge/msedgedriver.exe").Path
$tauriPath = (Resolve-Path "$ToolsDirectory/tauri/bin/tauri-driver.exe").Path
$driverProcess = $null
$nodeProcess = $null
$report = [ordered]@{ kind = 'windows-webview2-native-smoke'; nativeTestsRun = 0; nativeTestsPassed = 0; cleanupStarted = $false; normalCloseVerified = $false; failurePhase = $null }
$phase = 'environment'
try {
    $before = Get-Snapshot
    if (@($before | Where-Object ExecutablePath -eq $appPath).Count -ne 0) { throw 'NATIVE_PREEXISTING_APP_REJECTED' }
    if (@(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445)).Count -ne 0) { throw 'NATIVE_PORT_ALREADY_IN_USE' }
    $driverProcess = Start-Process $tauriPath -ArgumentList @('--port', '4444', '--native-port', '4445', '--native-driver', "`"$edgePath`"") -PassThru -RedirectStandardOutput "$ToolsDirectory/driver.stdout.log" -RedirectStandardError "$ToolsDirectory/driver.stderr.log"
    $until = [DateTime]::UtcNow.AddSeconds(15)
    $ready = $false
    do {
        if ($driverProcess.HasExited) { throw 'NATIVE_DRIVER_START_FAILED' }
        try { $ready = (Invoke-RestMethod 'http://127.0.0.1:4444/status' -TimeoutSec 2).value.ready } catch { Start-Sleep -Milliseconds 200 }
    } while (-not $ready -and [DateTime]::UtcNow -lt $until)
    if (-not $ready) { throw 'NATIVE_DRIVER_READY_TIMEOUT' }
    $snapshot = Get-Snapshot
    $driver = @($snapshot | Where-Object ProcessId -eq $driverProcess.Id)
    $edge = @($snapshot | Where-Object { $_.ParentProcessId -eq $driverProcess.Id -and $_.ExecutablePath -eq $edgePath })
    if ($driver.Count -ne 1 -or $edge.Count -ne 1 -or $driverProcess.HasExited -or $driver[0].ExecutablePath -ne $tauriPath) { throw 'NATIVE_DRIVER_IDENTITY_AMBIGUOUS' }
    $listeners = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445))
    $report.listenerFacts = @($listeners | ForEach-Object {
        $address = [Net.IPAddress]::Parse($_.LocalAddress)
        [ordered]@{ port = $_.LocalPort; loopback = [Net.IPAddress]::IsLoopback($address); family = $address.AddressFamily.ToString(); owned = $_.OwningProcess -eq $(if ($_.LocalPort -eq 4444) { $driver[0].ProcessId } else { $edge[0].ProcessId }) }
    })
    Assert-ListenerSnapshot $listeners $driver[0].ProcessId $edge[0].ProcessId
    $phase = 'webview-and-ipc'
    $report.nativeTestsRun = 1
    $env:NATIVE_OUTPUT_DIR = $OutputDirectory
    $env:NATIVE_APP_EXE = $appPath
    $env:NATIVE_PROFILE = Join-Path $ToolsDirectory 'webview-profile'
    $nodeProcess = Start-Process (Get-Command node.exe).Source -ArgumentList "$PSScriptRoot/run.ts" -PassThru -NoNewWindow -RedirectStandardOutput "$ToolsDirectory/test.stdout.log" -RedirectStandardError "$ToolsDirectory/test.stderr.log"
    if (-not $nodeProcess.WaitForExit(80000)) { throw 'NATIVE_TEST_PROCESS_TIMEOUT' }
    if ($nodeProcess.ExitCode -ne 0) { throw 'NATIVE_WEBVIEW_OR_IPC_ASSERTION_FAILED' }
    $view = Get-Content "$OutputDirectory/webview-result.json" -Raw | ConvertFrom-Json
    if ($view.browserVersion -ne (Get-Content "$PSScriptRoot/tools.json" -Raw | ConvertFrom-Json).runtimeVersion) { throw 'NATIVE_ACTUAL_RUNTIME_MISMATCH' }
    if ($view.nativeTestsStarted -ne 1 -or $view.checks -ne 3 -or -not $view.actualRustIpc) { throw 'NATIVE_ZERO_OR_INCOMPLETE_TESTS' }
    $snapshot = Get-Snapshot
    $observation = Get-OwnedApp $driver[0] $edge[0] $appPath $snapshot
    $app = $observation.App
    $children = $observation.Children
    $report.webviewChildrenObserved = $observation.Webviews.Count
    $phase = 'normal-close'
    $appProcess = Get-Process -Id $app.ProcessId -ErrorAction Stop
    $appProcess.Refresh()
    Request-NormalClose $app $appProcess 10000
    $until = [DateTime]::UtcNow.AddSeconds(10)
    do {
        $snapshot = Get-Snapshot
        Require-Live $driver[0] $snapshot
        Require-Live $edge[0] $snapshot
        $remaining = @($children | Where-Object {
            $previous = $_
            @($snapshot | Where-Object { Same-Process $previous $_ }).Count -gt 0
        })
        if ($remaining.Count -gt 0) { Start-Sleep -Milliseconds 200 }
    } while ($remaining.Count -gt 0 -and [DateTime]::UtcNow -lt $until)
    if ($remaining.Count -ne 0) { throw 'NATIVE_OBSERVED_CHILD_CLOSE_TIMEOUT' }
    if ($report.cleanupStarted) { throw 'NATIVE_CLOSE_OBSERVED_AFTER_CLEANUP' }
    $report.closeObservedBeforeCleanup = $true
    $report.normalCloseVerified = $true
    $report.appExitCode = $appProcess.ExitCode
    $report.nativeTestsPassed = 1
    $report.driverStableThroughClose = $true
    $report.browserVersion = $view.browserVersion
    Write-Output 'NATIVE_NORMAL_CLOSE_AND_OBSERVED_PROCESS_EXIT_VERIFIED'
} catch {
    $report.failurePhase = $phase
    $report.failureCode = if ($_.Exception.Message -match '^NATIVE_[A-Z_]+$') { $_.Exception.Message } else { 'NATIVE_ENVIRONMENT_OR_HARNESS_ERROR' }
    $report.failureType = $_.Exception.GetType().Name
    $report.failureScript = Split-Path $_.InvocationInfo.ScriptName -Leaf
    $report.failureLine = $_.InvocationInfo.ScriptLineNumber
    Write-Output $report.failureCode
} finally {
    # Cleanup is deliberately after the normal-close verdict. Driver Job Object also contains partial app launches.
    $report.cleanupStarted = $true
    if ($nodeProcess -and -not $nodeProcess.HasExited) { $nodeProcess.Kill() }
    if ($driverProcess -and -not $driverProcess.HasExited) { $driverProcess.Kill() }
    $report | ConvertTo-Json -Depth 4 | Set-Content "$OutputDirectory/native-result.json" -Encoding utf8
}
if ($report.nativeTestsRun -ne 1 -or $report.nativeTestsPassed -ne 1 -or -not $report.normalCloseVerified) { exit 1 }

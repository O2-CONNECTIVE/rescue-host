$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/processes.ps1"
function Entry($Number, $Parent, $Path, $Name) {
    return [pscustomobject]@{ ProcessId = $Number; ParentProcessId = $Parent; ExecutablePath = $Path; Name = $Name; CreationDate = [DateTime]::Parse('2026-10-27T00:00:00Z').ToUniversalTime() }
}
$driver = Entry 10 1 'driver.exe' 'driver.exe'
$edge = Entry 11 10 'edge.exe' 'edge.exe'
$app = Entry 12 11 'app.exe' 'app.exe'
$webview = Entry 13 12 'webview.exe' 'msedgewebview2.exe'
$other = Entry 14 1 'app.exe' 'app.exe'
$valid = @($driver, $edge, $app, $webview, $other)
$observed = Get-OwnedApp $driver $edge 'app.exe' $valid
if ($observed.App.ProcessId -ne 12 -or $observed.Webviews.Count -ne 1) { throw 'PROCESS_CONTRACT_POSITIVE_FAILED' }
$script:checks = 1
function Reject($Body, [string]$Expected) {
    $rejected = $false
    try { & $Body | Out-Null } catch {
        if ($_.Exception.Message -ne $Expected) { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw 'PROCESS_CONTRACT_FALSE_SUCCESS' }
    $script:checks += 1
}
Reject { Get-OwnedApp $driver $edge 'wrong.exe' $valid } 'NATIVE_APP_OWNERSHIP_AMBIGUOUS'
Reject { Get-OwnedApp $driver $edge 'app.exe' @($driver, $edge, $app) } 'NATIVE_WEBVIEW_CHILD_NOT_OBSERVED'
Reject { Get-OwnedApp $driver $edge 'app.exe' @($edge, $app, $webview) } 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED'
$reused = Entry 10 1 'driver.exe' 'driver.exe'
$reused.CreationDate = 'different-creation'
Reject { Get-OwnedApp $driver $edge 'app.exe' @($reused, $edge, $app, $webview) } 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED'
$fake = [pscustomobject]@{ ExitCode = 0; Id = 12; Path = 'app.exe'; HasExited = $false; SafeHandle = 1; StartTime = $app.CreationDate }
$fake | Add-Member ScriptMethod CloseMainWindow { return $true }
$fake | Add-Member ScriptMethod WaitForExit { param($TimeoutMs) return $false }
Reject { Request-NormalClose $app $fake 1 } 'NATIVE_APP_CLOSE_TIMEOUT'
$fake.StartTime = $app.CreationDate.AddSeconds(1)
Reject { Request-NormalClose $app $fake 1 } 'NATIVE_CLOSE_TARGET_IDENTITY_MISMATCH'
$fake.StartTime = $app.CreationDate
$fake.Path = 'unowned.exe'
Reject { Request-NormalClose $app $fake 1 } 'NATIVE_CLOSE_TARGET_IDENTITY_MISMATCH'
$loop4 = [pscustomobject]@{ LocalPort = 4444; LocalAddress = '127.0.0.1'; OwningProcess = 10 }
$edge4 = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '127.0.0.1'; OwningProcess = 11 }
$edge6 = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '::1'; OwningProcess = 11 }
Assert-ListenerSnapshot @($loop4, $edge4, $edge6) 10 11
$checks += 1
$wildcard = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '0.0.0.0'; OwningProcess = 11 }
Reject { Assert-ListenerSnapshot @($loop4, $wildcard) 10 11 } 'NATIVE_LISTENER_OWNERSHIP_MISMATCH'
$unowned = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '::1'; OwningProcess = 99 }
Reject { Assert-ListenerSnapshot @($loop4, $unowned) 10 11 } 'NATIVE_LISTENER_OWNERSHIP_MISMATCH'
Reject { Assert-ListenerSnapshot @($loop4) 10 11 } 'NATIVE_LISTENER_OWNERSHIP_MISMATCH'
if ($checks -ne 12) { throw 'PROCESS_CONTRACT_ZERO_OR_INCOMPLETE_TESTS' }
Write-Output "PROCESS_CONTRACT_PASS checks=$checks (fixtures only; not native evidence)"

# 終了の合否を変えず、OS観測の不一致を保持する公開harness境界。
. "$PSScriptRoot/close-observation.ps1"
$handle = [pscustomobject]@{ Slot = 1; Identity = 'verified'; Process = [pscustomobject]@{ HasExited = $false } }
$facts = Get-ChildCloseFacts @($webview) $app $valid @($handle)
if ($facts.observed -ne 1 -or $facts.remainingSnapshot -ne 1 -or $facts.rows[0].snapshotState -ne 'same-identity' -or $facts.rows[0].handleState -ne 'live') { throw 'CLOSE_FACTS_LIVE_NOT_RECORDED' }
$handle.Process.HasExited = $true
$facts = Get-ChildCloseFacts @($webview) $app $valid @($handle)
if ($facts.rows[0].snapshotState -ne 'same-identity' -or $facts.rows[0].handleState -ne 'exited') { throw 'CLOSE_FACTS_DISAGREEMENT_LOST' }
if ($facts.PSObject.Properties.Name -contains 'normalCloseVerified') { throw 'CLOSE_FACTS_FALSE_VERDICT' }
$handle.Identity = 'unavailable'
$facts = Get-ChildCloseFacts @($webview) $app $valid @($handle)
if ($facts.rows[0].handleState -ne 'unavailable' -or $facts.remainingSnapshot -ne 1) { throw 'CLOSE_FACTS_UNAVAILABLE_WAS_SUCCESS' }
$old = Entry 15 12 'PRIVATE_CHILD_PATH.exe' 'other.exe'
$old.CreationDate = $app.CreationDate.AddSeconds(-1)
$facts = Get-ChildCloseFacts @($old) $app @($old) @()
if ($facts.observed -ne 1 -or $facts.remainingSnapshot -ne 1 -or $facts.rows[0].parentOrder -ne 'child-older' -or $facts.rows[0].role -ne 'other') { throw 'CLOSE_FACTS_CHILD_WAS_FILTERED' }
$json = $facts | ConvertTo-Json -Depth 5
if ($json.Contains('PRIVATE_CHILD_PATH') -or $json.Contains('2026-10-27') -or $json.Contains('ProcessId')) { throw 'CLOSE_FACTS_PRIVATE_METADATA_LEAK' }
Write-Output 'CLOSE_OBSERVATION_CONTRACT_PASS checks=5 (fixtures only; not native evidence)'

# cleanupのOS操作が失敗しても、元FAILの保存と所有handle解放を試す。
$cleanupFolder = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $cleanupFolder -ErrorAction Stop | Out-Null
try {
    $broken = [pscustomobject]@{ HasExited = $false }
    $broken | Add-Member ScriptMethod Kill { throw 'PRIVATE_CLEANUP_FAILURE' }
    $broken | Add-Member ScriptMethod WaitForExit { param($Ms) throw 'PRIVATE_WAIT_FAILURE' }
    $disposable = [pscustomobject]@{ Disposed = $false }
    $disposable | Add-Member ScriptMethod Dispose { $this.Disposed = $true }
    $cleanupReport = [ordered]@{ normalCloseVerified = $false; nativeTestsRun = 1; nativeTestsPassed = 0 }
    Complete-NativeCleanup $broken $broken @() $app @([pscustomobject]@{ Process = $disposable }) $cleanupReport $cleanupFolder
    $saved = Get-Content (Join-Path $cleanupFolder 'native-result.json') -Raw | ConvertFrom-Json
    if (-not $disposable.Disposed -or $saved.normalCloseVerified -or $saved.nativeTestsPassed -ne 0 -or $saved.cleanupFailures -lt 1) { throw 'CLEANUP_FAILURE_LOST_VERDICT_OR_HANDLE' }
    if (($saved | ConvertTo-Json -Depth 6).Contains('PRIVATE_')) { throw 'CLEANUP_PRIVATE_EXCEPTION_LEAK' }
} finally { Remove-Item -LiteralPath $cleanupFolder -Recurse -Force }
Write-Output 'CLEANUP_FAILURE_CONTRACT_PASS checks=2 (fixtures only; not native evidence)'

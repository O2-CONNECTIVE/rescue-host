$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/processes.ps1"
function Entry($Number, $Parent, $Path, $Name) {
    return [pscustomobject]@{ ProcessId = $Number; ParentProcessId = $Parent; ExecutablePath = $Path; Name = $Name; CreationDate = 'fixed-creation' }
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
$fake = [pscustomobject]@{ ExitCode = 0 }
$fake | Add-Member ScriptMethod CloseMainWindow { return $true }
$fake | Add-Member ScriptMethod WaitForExit { param($TimeoutMs) return $false }
Reject { Request-NormalClose $fake 1 } 'NATIVE_APP_CLOSE_TIMEOUT'
if ($checks -ne 6) { throw 'PROCESS_CONTRACT_ZERO_OR_INCOMPLETE_TESTS' }
Write-Output "PROCESS_CONTRACT_PASS checks=$checks (fixtures only; not native evidence)"

function Get-Snapshot { return @(Get-CimInstance Win32_Process -ErrorAction Stop) }
function Same-Process($Expected, $Actual) {
    return $null -ne $Actual -and $Expected.ProcessId -eq $Actual.ProcessId -and $Expected.CreationDate -eq $Actual.CreationDate
}
function Require-Live($Expected, $Snapshot) {
    $current = @($Snapshot | Where-Object ProcessId -eq $Expected.ProcessId)
    if ($current.Count -ne 1 -or -not (Same-Process $Expected $current[0])) { throw 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED' }
}
function Get-Descendants([int]$RootId, $Snapshot) {
    $found = @()
    $frontier = @($RootId)
    while ($frontier.Count -gt 0) {
        $next = @($Snapshot | Where-Object { $_.ParentProcessId -in $frontier -and $_.ProcessId -notin @($found | ForEach-Object ProcessId) })
        $found += $next
        $frontier = @($next | ForEach-Object ProcessId)
    }
    return $found
}
function Assert-Listeners([int]$DriverId, [int]$EdgeId) {
    $listeners = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445))
    foreach ($pair in @(@(4444, $DriverId), @(4445, $EdgeId))) {
        $match = @($listeners | Where-Object LocalPort -eq $pair[0])
        if ($match.Count -ne 1 -or $match[0].LocalAddress -ne '127.0.0.1' -or $match[0].OwningProcess -ne $pair[1]) { throw 'NATIVE_LISTENER_OWNERSHIP_MISMATCH' }
    }
}
function Get-OwnedApp($Driver, $Edge, [string]$AppPath, $Snapshot) {
    Require-Live $Driver $Snapshot
    Require-Live $Edge $Snapshot
    $owned = @(Get-Descendants $Driver.ProcessId $Snapshot)
    $apps = @($owned | Where-Object ExecutablePath -eq $AppPath)
    if ($apps.Count -ne 1) { throw 'NATIVE_APP_OWNERSHIP_AMBIGUOUS' }
    $children = @(Get-Descendants $apps[0].ProcessId $Snapshot)
    $webviews = @($children | Where-Object Name -eq 'msedgewebview2.exe')
    if ($webviews.Count -lt 1) { throw 'NATIVE_WEBVIEW_CHILD_NOT_OBSERVED' }
    return [pscustomobject]@{ App = $apps[0]; Children = $children; Webviews = $webviews }
}
function Request-NormalClose($Process, [int]$TimeoutMs) {
    if (-not $Process.CloseMainWindow()) { throw 'NATIVE_NORMAL_CLOSE_REQUEST_REJECTED' }
    if (-not $Process.WaitForExit($TimeoutMs)) { throw 'NATIVE_APP_CLOSE_TIMEOUT' }
    if ($Process.ExitCode -ne 0) { throw 'NATIVE_APP_ABNORMAL_EXIT' }
}

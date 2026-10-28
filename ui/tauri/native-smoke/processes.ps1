function Get-Snapshot { return @(Get-CimInstance Win32_Process -ErrorAction Stop) }
function Same-Process($Expected, $Actual) {
    return $null -ne $Actual -and $Expected.ProcessId -eq $Actual.ProcessId -and $Expected.CreationDate -eq $Actual.CreationDate
}
function Require-Live($Expected, $Snapshot) {
    $current = @($Snapshot | Where-Object ProcessId -eq $Expected.ProcessId)
    if ($current.Count -ne 1 -or -not (Same-Process $Expected $current[0])) { throw 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED' }
}
function Test-LineageCreation($Entry) {
    return $null -ne $Entry -and $null -ne $Entry.PSObject.Properties['CreationDate'] -and $Entry.CreationDate -is [DateTime]
}
function Stop-UnconfirmedLineage($Evidence, [string]$Reason, $Parent, $Child) {
    if ($null -ne $Evidence) {
        $parentKnown = Test-LineageCreation $Parent
        $childKnown = Test-LineageCreation $Child
        $delta = if ($parentKnown -and $childKnown) { [Math]::Round(($Child.CreationDate - $Parent.CreationDate).TotalMilliseconds, 3) } else { $null }
        $Evidence.Value += [pscustomobject]@{
            reason = $Reason; parentCreationKnown = $parentKnown; childCreationKnown = $childKnown
            childMinusParentCreationMs = $delta
            childRole = $(if ($Child -and $Child.Name -eq 'msedgewebview2.exe') { 'webview' } else { 'other' })
        }
    }
    throw 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
}
function Get-Descendants([int]$RootId, $Snapshot, $LineageEvidence = $null) {
    $roots = @($Snapshot | Where-Object ProcessId -eq $RootId)
    if ($roots.Count -ne 1) { Stop-UnconfirmedLineage $LineageEvidence 'parent-row-ambiguous' $null $null }
    if (-not (Test-LineageCreation $roots[0])) { Stop-UnconfirmedLineage $LineageEvidence 'parent-creation-unavailable' $roots[0] $null }
    $found = @()
    $frontier = @($roots[0])
    while ($frontier.Count -gt 0) {
        $next = @()
        foreach ($parent in $frontier) {
            $candidates = @($Snapshot | Where-Object { $_.ParentProcessId -eq $parent.ProcessId -and $_.ProcessId -notin @($found | ForEach-Object ProcessId) })
            foreach ($child in $candidates) {
                if ($child.ProcessId -eq $RootId -or @($Snapshot | Where-Object ProcessId -eq $child.ProcessId).Count -ne 1) {
                    Stop-UnconfirmedLineage $LineageEvidence 'child-row-ambiguous' $parent $child
                }
                if (-not (Test-LineageCreation $child)) { Stop-UnconfirmedLineage $LineageEvidence 'child-creation-unavailable' $parent $child }
                # 時刻逆順を非所有確定として除外しない。時計変動も含め探索全体を不確定にする。
                if ($child.CreationDate -lt $parent.CreationDate) { Stop-UnconfirmedLineage $LineageEvidence 'child-older' $parent $child }
                $found += $child
                $next += $child
            }
        }
        $frontier = $next
    }
    return $found
}
function Assert-ListenerSnapshot($Listeners, [int]$DriverId, [int]$EdgeId) {
    foreach ($pair in @(@(4444, $DriverId), @(4445, $EdgeId))) {
        $matches = @($Listeners | Where-Object LocalPort -eq $pair[0])
        if ($matches.Count -eq 0) { throw 'NATIVE_LISTENER_OWNERSHIP_MISMATCH' }
        foreach ($listener in $matches) {
            if (-not [Net.IPAddress]::IsLoopback([Net.IPAddress]::Parse($listener.LocalAddress)) -or $listener.OwningProcess -ne $pair[1]) { throw 'NATIVE_LISTENER_OWNERSHIP_MISMATCH' }
        }
    }
}
function Get-OwnedApp($Driver, $Edge, [string]$AppPath, $Snapshot, $LineageEvidence = $null) {
    if ($null -ne $LineageEvidence -and $LineageEvidence -isnot [System.Management.Automation.PSReference]) { throw 'NATIVE_LINEAGE_OWNER_UNCONFIRMED' }
    Require-Live $Driver $Snapshot
    Require-Live $Edge $Snapshot
    $owned = @(Get-Descendants $Driver.ProcessId $Snapshot $LineageEvidence)
    $apps = @($owned | Where-Object ExecutablePath -eq $AppPath)
    if ($apps.Count -ne 1) { throw 'NATIVE_APP_OWNERSHIP_AMBIGUOUS' }
    $children = @(Get-Descendants $apps[0].ProcessId $Snapshot $LineageEvidence)
    $webviews = @($children | Where-Object Name -eq 'msedgewebview2.exe')
    if ($webviews.Count -lt 1) { throw 'NATIVE_WEBVIEW_CHILD_NOT_OBSERVED' }
    return [pscustomobject]@{ App = $apps[0]; Children = $children; Webviews = $webviews }
}
function Assert-CloseTarget($Recorded, $Process) {
    # Open the original process handle before reading identity; never act on PID alone.
    $null = $Process.SafeHandle
    $expectedTime = $Recorded.CreationDate.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')
    $actualTime = $Process.StartTime.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')
    if ($Process.Id -ne $Recorded.ProcessId -or $Process.HasExited -or $Process.Path -ne $Recorded.ExecutablePath -or $actualTime -ne $expectedTime) { throw 'NATIVE_CLOSE_TARGET_IDENTITY_MISMATCH' }
}
function Request-NormalClose($Recorded, $Process, [int]$TimeoutMs) {
    Assert-CloseTarget $Recorded $Process
    if (-not $Process.CloseMainWindow()) { throw 'NATIVE_NORMAL_CLOSE_REQUEST_REJECTED' }
    if (-not $Process.WaitForExit($TimeoutMs)) { throw 'NATIVE_APP_CLOSE_TIMEOUT' }
    if ($Process.ExitCode -ne 0) { throw 'NATIVE_APP_ABNORMAL_EXIT' }
}

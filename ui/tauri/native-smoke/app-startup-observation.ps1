# 試験専用。UI/要求の期限を変えず、既存diagnostic waitの1秒budgetだけを共有する。
function Wait-AppStartupTrace([string]$Directory, [Diagnostics.Stopwatch]$Budget) {
    if (-not $Budget.IsRunning) { $Budget.Start() }
    try {
        [IO.File]::WriteAllText((Join-Path $Directory 'app-startup.finish'), 'finish')
        $file = Join-Path $Directory 'app-startup-stages.json'
        do {
            $stream = $null
            try {
                # Rust writerが閉じるまで途中書込みを完成物として扱わない。
                $stream = [IO.File]::Open($file, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
                return $stream.Length -gt 0 -and $stream.Length -le 2048
            } catch [IO.IOException] { }
            finally { if ($null -ne $stream) { $stream.Dispose() } }
            if ($Budget.ElapsedMilliseconds -ge 1000) { break }
            Start-Sleep -Milliseconds 20
        } while ($Budget.ElapsedMilliseconds -lt 1000)
        return $false
    } catch { return $false }
    finally { $Budget.Stop() }
}

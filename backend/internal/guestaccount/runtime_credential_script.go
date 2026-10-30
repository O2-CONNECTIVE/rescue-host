package guestaccount

// Only the fixed helper reads this finite binary input. The public correlation
// frame contains no secret; secret bytes are never converted to a managed String.
func runtimeCredentialScript(module string) string {
	return `$ErrorActionPreference='Stop'
$ConfirmPreference='None'
$WhatIfPreference=$false
$ProgressPreference='SilentlyContinue'
$PSModuleAutoLoadingPreference='None'
$secret=$null
$password=$null
try {
  $inputStream=[Console]::OpenStandardInput()
  function Read-Exact([int]$count) {
    $bytes=[byte[]]::new($count)
    $offset=0
    while ($offset -lt $count) {
      $n=$inputStream.Read($bytes,$offset,$count-$offset)
      if ($n -le 0) { throw 'frame-incomplete' }
      $offset+=$n
    }
    return ,$bytes
  }
  $header=Read-Exact 14
  if ([System.Text.Encoding]::ASCII.GetString($header,0,6) -cne "RHGC1` + "`n" + `") { throw 'frame-protocol' }
  $length=[BitConverter]::ToUInt32($header,6)
  if ($length -eq 0 -or $length -gt 16384 -or [BitConverter]::ToUInt32($header,10) -ne 36) { throw 'frame-size' }
  $record=Read-Exact $length
  $text=[System.Text.UTF8Encoding]::new($false,$true).GetString($record)
  $pattern='\A\{"protocol":"rescue-host-guest-credential/1","operation":"prepare-runtime","epoch":"[0-9a-f]{64}","intent":"[0-9a-f]{64}","policy":"[0-9a-f]{64}","generation":"(?<generation>[1-9][0-9]{0,19})","machine":"(?<machine>S-1-5-21-[0-9]+-[0-9]+-[0-9]+)","operator":"(?<operator>S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+)","service":"(?<service>S-1-5-(?:18|19|20|21-[0-9]+-[0-9]+-[0-9]+-[0-9]+|80-[0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+))","name":"(?<name>rhg_[0-9a-f]{16})","sid":"(?<sid>S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+)"\}\n\z'
  $match=[regex]::Match($text,$pattern,[System.Text.RegularExpressions.RegexOptions]::CultureInvariant)
  if (!$match.Success) { throw 'frame-shape' }
  $null=[uint64]::Parse($match.Groups['generation'].Value,[Globalization.CultureInfo]::InvariantCulture)
  $sid=[System.Security.Principal.SecurityIdentifier]::new($match.Groups['sid'].Value)
  if ($sid.AccountDomainSid.Value -cne $match.Groups['machine'].Value -or $sid.Value -ceq $match.Groups['operator'].Value -or $sid.Value -ceq $match.Groups['service'].Value) { throw 'sid-binding' }
  $secret=[byte[]]::new(72)
  $offset=0
  while ($offset -lt 72) {
    $n=$inputStream.Read($secret,$offset,72-$offset)
    if ($n -le 0) { throw 'secret-incomplete' }
    $offset+=$n
  }
  if ($inputStream.ReadByte() -ne -1) { throw 'frame-tail' }
` + sidScriptImport(module) + `  $user=@(Microsoft.PowerShell.LocalAccounts\Get-LocalUser -SID $sid -ErrorAction Stop)
  if ($user.Count -ne 1 -or $null -eq $user[0].SID -or $user[0].SID.Value -cne $sid.Value -or $user[0].Name -cne $match.Groups['name'].Value -or $user[0].Enabled -isnot [bool] -or $user[0].Enabled) { throw 'account-binding' }
  $password=[System.Security.SecureString]::new()
  for ($i=0;$i -lt 36;$i++) {
    $code=[BitConverter]::ToUInt16($secret,$i*2)
    if ($code -lt 33 -or $code -gt 126) { throw 'secret-shape' }
    $password.AppendChar([char]$code)
  }
  $password.MakeReadOnly()
  Microsoft.PowerShell.LocalAccounts\Set-LocalUser -SID $sid -Password $password -UserMayChangePassword:$false -ErrorAction Stop -WhatIf:$false -Confirm:$false
  Microsoft.PowerShell.LocalAccounts\Enable-LocalUser -SID $sid -ErrorAction Stop -WhatIf:$false -Confirm:$false
  $reply='{"protocol":"rescue-host-guest-result/1","request":'+$text.Substring(0,$text.Length-1)+',"outcome":"ok"}'+[char]10
  $bytes=[System.Text.UTF8Encoding]::new($false,$true).GetBytes($reply)
  $outputStream=[Console]::OpenStandardOutput()
  $outputStream.Write($bytes,0,$bytes.Length)
  $outputStream.Flush()
} catch { exit 1 }
finally {
  if ($null -ne $secret) { [Array]::Clear($secret,0,$secret.Length) }
  if ($null -ne $password) { $password.Dispose() }
}
`
}

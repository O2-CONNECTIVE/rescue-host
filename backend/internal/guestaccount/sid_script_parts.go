package guestaccount

import "strings"

// 固定二処理だけが共有するframe読取り。実EOFより前にimport/変更しない。
const sidScriptRead = `$ErrorActionPreference='Stop'
$ConfirmPreference='None'
$WhatIfPreference=$false
$ProgressPreference='SilentlyContinue'
$PSModuleAutoLoadingPreference='None'
try {
  $inputStream=[Console]::OpenStandardInput()
  $buffer=[byte[]]::new(1024)
  $record=[System.IO.MemoryStream]::new()
  while (($n=$inputStream.Read($buffer,0,$buffer.Length)) -gt 0) {
    if ($record.Length+$n -gt 16384) { throw 'frame-limit' }
    $record.Write($buffer,0,$n)
  }
  $text=[System.Text.UTF8Encoding]::new($false,$true).GetString($record.ToArray())
`

func sidScriptImport(module string) string {
	return "  Import-Module -Name '" + strings.ReplaceAll(module, "'", "''") + "' -Force -ErrorAction Stop\n"
}

const sidScriptEnd = `} catch {
  exit 1
}
`

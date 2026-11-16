# indesk.ps1 -Script <path.ps1> [-TimeoutSec N]
# Runs a script inside the logged-on desktop session of the current user (not
# the SSH session) through a one-off interactive task, waits for it, prints
# its output. Credential Manager and window visibility only behave like a real
# desktop there.
param([Parameter(Mandatory)][string]$Script, [int]$TimeoutSec = 300)
$ErrorActionPreference = 'Stop'
$id = 'e5probe-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$out = Join-Path $env:TEMP "$id.out"
$done = Join-Path $env:TEMP "$id.done"
$wrapper = Join-Path $env:TEMP "$id.ps1"
@"
try { & '$Script' *>&1 | Out-File -FilePath '$out' -Encoding utf8 } catch { `$_ | Out-File -FilePath '$out' -Append -Encoding utf8 }
Set-Content -Path '$done' -Value ok
"@ | Set-Content -Path $wrapper -Encoding utf8
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$wrapper`""
$principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $id -Action $action -Principal $principal | Out-Null
Start-ScheduledTask -TaskName $id
$deadline = (Get-Date).AddSeconds($TimeoutSec)
while (-not (Test-Path $done) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
Unregister-ScheduledTask -TaskName $id -Confirm:$false
if (Test-Path $out) { Get-Content $out }
if (-not (Test-Path $done)) { Write-Output "TIMEOUT after $TimeoutSec s"; exit 1 }
Remove-Item $out, $done, $wrapper -ErrorAction SilentlyContinue

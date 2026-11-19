# Installs the monitoring agent on a Windows laptop so it starts with Windows and restarts if it stops.
# Run in an elevated PowerShell, from the folder that holds stream-agent-windows.exe:
#
#   .\install-windows.ps1 -RemoteWrite https://prometheus.example.org/api/v1/write `
#                         -Hub "Ujjain" -Temple "Temple A" -Name "Main hall 1" `
#                         -Interval 5s `
#                         -Username <user> -Password <password>
#
# -Interval defaults to 15s. A shorter interval does not shrink the offline buffer:
# the agent keeps ~8 hours of readings whatever the interval.
#
# -SpeedtestInterval 1m downloads 10 MB a minute and reports the real download speed, so a weak
# connection is visible even when nothing else is using it. That is about 14 GB per laptop per day:
# raise the interval or lower -SpeedtestBytes to cut it. 0 (the default) turns it off entirely.
#
# -ObsPassword turns on OBS metrics (dropped frames, reconnects, real stream bitrate). Enable the
# server first in OBS: Tools > WebSocket Server Settings > Enable WebSocket server. Without it the
# agent behaves exactly as before, so it is safe to omit on laptops that do not run OBS.
#
# NOTE: not yet run on a real Windows laptop. Try it on one machine first.
# The laptop's clock must be correct (Windows: Settings > Time & language > Sync now), or readings are rejected.
param(
  [Parameter(Mandatory)] [string] $RemoteWrite,
  [Parameter(Mandatory)] [string] $Hub,
  [Parameter(Mandatory)] [string] $Temple,
  [string] $Name = $env:COMPUTERNAME,
  [string] $Interval = "15s",          # how often to measure, e.g. 5s, 15s, 1m
  [string] $ObsPassword = "",          # OBS > Tools > WebSocket Server Settings; enables OBS metrics
  [string] $ObsUrl = "ws://127.0.0.1:4455",
  [string] $WifiInterval = "30s",      # how often to read the Wi-Fi link rate; 0 turns it off
  [string] $SpeedtestInterval = "0",   # e.g. 1m to measure real download speed; 0 turns it off
  [int]    $SpeedtestBytes = 10,       # megabytes per speed test
  [string] $Username = "",
  [string] $Password = "",
  [string] $Exe = "$PSScriptRoot\stream-agent-windows.exe"
)
$ErrorActionPreference = "Stop"
$dir = "C:\ProgramData\StreamMonitor"
$task = "StreamMonitorAgent"
New-ScheduledTask -Action (New-ScheduledTaskAction -Execute "cmd.exe") | Out-Null   # fails early if not elevated

Stop-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $dir | Out-Null
Copy-Item $Exe "$dir\stream-agent.exe" -Force

$agentArgs = "--remote-write `"$RemoteWrite`" --hub `"$Hub`" --temple `"$Temple`" --name `"$Name`" --interval `"$Interval`" --wifi-interval `"$WifiInterval`" --speedtest-interval `"$SpeedtestInterval`" --speedtest-bytes $SpeedtestBytes"
if ($Username)    { $agentArgs += " --username `"$Username`" --password `"$Password`"" }
if ($ObsPassword) { $agentArgs += " --obs-password `"$ObsPassword`" --obs-url `"$ObsUrl`"" }
$action   = New-ScheduledTaskAction -Execute "$dir\stream-agent.exe" -Argument $agentArgs -WorkingDirectory $dir
$trigger  = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet -RestartCount 99 -RestartInterval (New-TimeSpan -Minutes 1) `
            -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName $task -Action $action -Trigger $trigger -Settings $settings -User "SYSTEM" -RunLevel Highest | Out-Null
Start-ScheduledTask -TaskName $task
Write-Host "Installed. The laptop should appear in Grafana within a minute."
Write-Host "To remove: Unregister-ScheduledTask -TaskName $task -Confirm:`$false; Remove-Item -Recurse $dir"

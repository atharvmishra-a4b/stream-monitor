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
# -SpeedtestUrl points the test at a file of your own instead of Cloudflare, which measures the path
# to your own infrastructure. Any URL serving range requests will do (S3 and CloudFront both do).
# Against a nearby CDN one connection saturates the line, so -SpeedtestParallel 1 is enough:
#   -SpeedtestUrl "https://srm-cdn.a4b.io/test/obs_network_bandwidth.mp4" -SpeedtestParallel 1
#
# -ConfigUrl points at a shared JSON file so the fleet can be retuned from one place. Anything set
# on this command line wins over it, so a single laptop can be pinned for debugging.
#
# -LogUrl ships OBS's own log file, which is what explains an incident after the fact.
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
  [string] $Interval = "",             # how often to measure, e.g. 5s. Blank: use the shared config.
  [string] $ObsPassword = "",          # OBS > Tools > WebSocket Server Settings; enables OBS metrics
  [string] $ObsUrl = "ws://127.0.0.1:4455",
  [string] $ObsLogDir = "",            # OBS log folder (default: find it under the user profiles)
  [string] $WifiInterval = "",         # how often to read the link rate; 0 turns it off
  [string] $ProgramsInterval = "",     # how often to report the top programs; 0 turns it off
  [string] $SpeedtestInterval = "",    # e.g. 1m to measure real download speed; 0 turns it off
  [string] $SpeedtestBytes = "",       # megabytes per test; 0 = whole file at -SpeedtestUrl
  [string] $SpeedtestParallel = "",    # connections per test; 1 is enough for a nearby CDN
  [string] $SpeedtestUrl = "",         # own CDN file; blank uses Cloudflare
  [string] $ConfigUrl = "",            # shared JSON config; settings there apply unless set here
  [string] $LogUrl = "",               # endpoint that receives OBS's log
  [string] $LogToken = "",             # sent as the x-api-key header
  [string] $LogAppName = "",           # sent as the x-afb-app-name header
  [string] $LogInterval = "",          # how often to send collected log lines
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

# Only pass a flag that was actually given. A flag on the command line deliberately overrides the
# shared config, so passing one unconditionally would pin that setting to the installer's default
# and make it impossible to change centrally.
$agentArgs = "--remote-write `"$RemoteWrite`" --hub `"$Hub`" --temple `"$Temple`" --name `"$Name`""
if ($Interval)          { $agentArgs += " --interval `"$Interval`"" }
if ($WifiInterval)      { $agentArgs += " --wifi-interval `"$WifiInterval`"" }
if ($ProgramsInterval)  { $agentArgs += " --programs-interval `"$ProgramsInterval`"" }
if ($SpeedtestInterval) { $agentArgs += " --speedtest-interval `"$SpeedtestInterval`"" }
if ($SpeedtestBytes)    { $agentArgs += " --speedtest-bytes $SpeedtestBytes" }
if ($SpeedtestParallel) { $agentArgs += " --speedtest-parallel $SpeedtestParallel" }
if ($SpeedtestUrl)      { $agentArgs += " --speedtest-url `"$SpeedtestUrl`"" }
if ($ConfigUrl)         { $agentArgs += " --config-url `"$ConfigUrl`"" }
if ($LogUrl)            { $agentArgs += " --log-url `"$LogUrl`"" }
if ($LogToken)          { $agentArgs += " --log-token `"$LogToken`"" }
if ($LogAppName)        { $agentArgs += " --log-app-name `"$LogAppName`"" }
if ($LogInterval)       { $agentArgs += " --log-interval `"$LogInterval`"" }
if ($ObsLogDir)         { $agentArgs += " --obs-log-dir `"$ObsLogDir`"" }
if ($Username)          { $agentArgs += " --username `"$Username`" --password `"$Password`"" }
if ($ObsPassword)       { $agentArgs += " --obs-password `"$ObsPassword`" --obs-url `"$ObsUrl`"" }
$action   = New-ScheduledTaskAction -Execute "$dir\stream-agent.exe" -Argument $agentArgs -WorkingDirectory $dir
$trigger  = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet -RestartCount 99 -RestartInterval (New-TimeSpan -Minutes 1) `
            -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName $task -Action $action -Trigger $trigger -Settings $settings -User "SYSTEM" -RunLevel Highest | Out-Null
Start-ScheduledTask -TaskName $task
Write-Host "Installed. The laptop should appear in Grafana within a minute."
Write-Host "To remove: Unregister-ScheduledTask -TaskName $task -Confirm:`$false; Remove-Item -Recurse $dir"

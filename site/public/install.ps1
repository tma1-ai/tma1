# TMA1 installer for Windows — downloads the latest tma1-server binary and registers a scheduled task.
#
# Install or upgrade:
#   irm https://tma1.ai/install.ps1 | iex
#
# Pin a specific version:
#   $env:TMA1_VERSION = 'v0.1.0'; irm https://tma1.ai/install.ps1 | iex
#
# Uninstall:
#   Unregister-ScheduledTask -TaskName 'TMA1 Server' -Confirm:$false
#   Remove-Item -Recurse -Force "$env:USERPROFILE\.tma1"

$ErrorActionPreference = 'Stop'

$Repo = 'tma1-ai/tma1'
$InstallDir = if ($env:TMA1_INSTALL_DIR) { $env:TMA1_INSTALL_DIR } else { Join-Path $env:USERPROFILE '.tma1\bin' }
$TMA1Port = if ($env:TMA1_PORT) { $env:TMA1_PORT } else { '14318' }
$TMA1DataDir = if ($env:TMA1_DATA_DIR) { $env:TMA1_DATA_DIR } else { Join-Path $env:USERPROFILE '.tma1' }

function Write-Info  { param([string]$msg) Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Warn  { param([string]$msg) Write-Host "Warning: $msg" -ForegroundColor Yellow }

# --- Resolve latest release tag ---
function Resolve-Version {
    if ($env:TMA1_VERSION) { return $env:TMA1_VERSION }

    Write-Info 'Resolving latest version...'
    try {
        # GitHub redirects /releases/latest to the tag URL.
        $resp = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" `
            -MaximumRedirection 0 -ErrorAction SilentlyContinue -UseBasicParsing
    } catch {
        $resp = $_.Exception.Response
    }
    if ($resp -and $resp.Headers -and $resp.Headers.Location) {
        $loc = $resp.Headers.Location
        if ($loc -is [System.Collections.IEnumerable] -and $loc -isnot [string]) { $loc = $loc[0] }
        if ($loc -match '(v\d+\.\d+\.\d+.*)$') { return $Matches[1] }
    }

    # Fallback: GitHub API
    $releases = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=1" -UseBasicParsing
    if ($releases -and $releases[0].tag_name) { return $releases[0].tag_name }

    throw 'Failed to resolve latest version. Set $env:TMA1_VERSION to install a specific version.'
}

# --- Stop existing service before upgrade ---
function Stop-ExistingService {
    $task = Get-ScheduledTask -TaskName 'TMA1 Server' -ErrorAction SilentlyContinue
    if (-not $task) { return }

    Write-Info 'Stopping existing TMA1 service...'

    # 1. Stop the scheduled task
    Stop-ScheduledTask -TaskName 'TMA1 Server' -ErrorAction SilentlyContinue

    # 2. Wait for the process launched from our install dir to exit.
    #    Match by executable path to avoid killing unrelated instances.
    $expectedPath = Join-Path $InstallDir 'tma1-server.exe'
    $retries = 0
    while ($retries -lt 30) {
        $proc = Get-Process -Name 'tma1-server' -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -eq $expectedPath }
        if (-not $proc) { break }
        Start-Sleep -Seconds 1
        $retries++
    }

    # 3. Force-kill if still running after 30s
    $proc = Get-Process -Name 'tma1-server' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $expectedPath }
    if ($proc) { $proc | Stop-Process -Force }

    # 4. Unregister old task
    Unregister-ScheduledTask -TaskName 'TMA1 Server' -Confirm:$false -ErrorAction SilentlyContinue
}

# --- Download and verify ---
function Install-TMA1 {
    param([string]$Version)

    $archive = "tma1-server-windows-amd64.tar.gz"
    $url = "https://github.com/$Repo/releases/download/$Version/$archive"
    $checksumUrl = "$url.sha256sum"

    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "tma1-install-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    $archivePath = Join-Path $tmpDir $archive

    try {
        Write-Info "Downloading $archive ($Version)..."
        Invoke-WebRequest -Uri $url -OutFile $archivePath -UseBasicParsing

        # Verify checksum
        Write-Info 'Verifying checksum...'
        try {
            $checksumFile = Join-Path $tmpDir 'checksum.txt'
            Invoke-WebRequest -Uri $checksumUrl -OutFile $checksumFile -UseBasicParsing
            $checksumLine = Get-Content $checksumFile | Where-Object { $_ -match $archive } | Select-Object -First 1
            if ($checksumLine) {
                $expectedHash = ($checksumLine -split '\s+')[0]
                $actualHash = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLower()
                if ($actualHash -ne $expectedHash) {
                    throw "Checksum mismatch: expected $expectedHash, got $actualHash"
                }
                Write-Info "Checksum verified."
            } else {
                Write-Warn 'Checksum entry not found, skipping verification.'
            }
        } catch [System.Net.WebException] {
            Write-Warn 'Checksum file not found, skipping verification.'
        }

        # Extract
        Write-Info "Extracting to $InstallDir..."
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        tar -xzf $archivePath -C $InstallDir
    } finally {
        Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
    }
}

# --- Register scheduled task (at logon, auto-restart) ---
function Register-TMA1Task {
    $binPath = Join-Path $InstallDir 'tma1-server.exe'
    if (-not (Test-Path $binPath)) {
        Write-Warn "Binary not found at $binPath. Skipping service registration."
        return
    }

    Write-Info 'Registering TMA1 as a scheduled task (runs at logon)...'

    # Pass runtime config as environment variables via cmd wrapper,
    # matching what the Unix installer does with launchd/systemd.
    $cmdArgs = "/c `"set `"TMA1_PORT=$TMA1Port`" && set `"TMA1_DATA_DIR=$TMA1DataDir`" && `"$binPath`"`""
    $action = New-ScheduledTaskAction -Execute 'cmd.exe' -Arguments $cmdArgs
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Seconds 10) `
        -ExecutionTimeLimit (New-TimeSpan -Days 9999)

    Register-ScheduledTask -TaskName 'TMA1 Server' `
        -Action $action -Trigger $trigger -Settings $settings `
        -Description 'TMA1 Server - LLM Agent Observability' `
        -Force | Out-Null

    # Start the task now
    Start-ScheduledTask -TaskName 'TMA1 Server'
    Write-Info 'TMA1 service started.'
}

# --- Wait for health endpoint ---
function Wait-ForHealth {
    $url = "http://127.0.0.1:${TMA1Port}/health"
    Write-Info 'Waiting for TMA1 to become ready...'
    for ($i = 0; $i -lt 30; $i++) {
        try {
            $resp = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 2 -ErrorAction SilentlyContinue
            if ($resp.StatusCode -eq 200) {
                Write-Info 'TMA1 is running and healthy.'
                return
            }
        } catch {}
        Start-Sleep -Seconds 1
    }
    Write-Warn "TMA1 did not become ready within 30s. Check the process for errors."
}

# --- Post-install hints ---
function Show-PostInstall {
    $binPath = Join-Path $InstallDir 'tma1-server.exe'
    Write-Info "Installed tma1-server to $binPath"
    Write-Host ''

    # PATH hint
    if (-not ($env:PATH -split ';' | Where-Object { $_ -eq $InstallDir })) {
        Write-Host 'Add TMA1 to your PATH (run once):'
        Write-Host ''
        Write-Host "  [Environment]::SetEnvironmentVariable('PATH', `"$InstallDir;`" + [Environment]::GetEnvironmentVariable('PATH', 'User'), 'User')"
        Write-Host ''
    }

    Write-Host "Configure your agent (e.g. Claude Code %USERPROFILE%\.claude\settings.json):"
    Write-Host ''
    Write-Host '  "env": {'
    Write-Host "    `"OTEL_EXPORTER_OTLP_ENDPOINT`": `"http://localhost:${TMA1Port}/v1/otlp`","
    Write-Host '    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",'
    Write-Host '    "OTEL_METRICS_EXPORTER": "otlp",'
    Write-Host '    "OTEL_LOGS_EXPORTER": "otlp"'
    Write-Host '  }'
    Write-Host ''
    Write-Host "Dashboard: http://localhost:${TMA1Port}"
    Write-Host ''
}

# --- Main ---
function main {
    Write-Info 'Installing TMA1...'
    $version = Resolve-Version
    Stop-ExistingService
    Install-TMA1 -Version $version
    Register-TMA1Task
    Wait-ForHealth
    Show-PostInstall
}

main

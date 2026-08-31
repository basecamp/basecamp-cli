$ErrorActionPreference = 'Stop'

try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
  # Ignore when the runtime manages TLS defaults.
}

# Environment options:
#   BASECAMP_VERSION      Specific version to install (default: latest)
#   BASECAMP_BIN_DIR      Where to install the binary
#   BASECAMP_SKIP_SETUP   Set to 1 to skip first-time setup (still runs
#                         `basecamp setup agents`)
#   BASECAMP_NONINTERACTIVE
#                         Set to 1 or true to use non-interactive setup
#   BASECAMP_SETUP_AGENT  Which coding agent(s) `setup agents` connects:
#                         claude | codex | all | none. Unset = auto-detect.
#                         Piped install sets it for the interpreter, not the fetch:
#                           $env:BASECAMP_SETUP_AGENT='codex'; irm https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.ps1 | iex
$Repo = 'basecamp/basecamp-cli'
$Version = $env:BASECAMP_VERSION
$SkipSetup = $env:BASECAMP_SKIP_SETUP
$BinDir = $env:BASECAMP_BIN_DIR

function Step([string]$Message) {
  Write-Host "  -> $Message"
}

function Info([string]$Message) {
  Write-Host "  + $Message" -ForegroundColor Green
}

function Warn([string]$Message) {
  Write-Warning $Message
}

function Test-TruthyEnvironmentValue([string]$Value) {
  return $Value -match '^(?i:1|true)$'
}

function Fail([string]$Message) {
  throw $Message
}

function Get-PlatformArch {
  $arch = $env:PROCESSOR_ARCHITECTURE
  if ($env:PROCESSOR_ARCHITEW6432) {
    $arch = $env:PROCESSOR_ARCHITEW6432
  }

  switch -Regex ($arch) {
    '^(AMD64|x86_64)$' { return 'amd64' }
    '^ARM64$' { return 'arm64' }
    default { Fail "Unsupported Windows architecture: $arch" }
  }
}

function Get-LatestVersion {
  Step 'Resolving latest release version...'

  # Follow the releases/latest redirect first to avoid GitHub API rate limits.
  # -MaximumRedirection 0 turns the expected 302 into a terminating error, so
  # we read Location off the caught response. Headers.Location is Uri on
  # PowerShell Core and string on Windows PowerShell 5.1, so coerce to string.
  $location = $null
  try {
    $response = Invoke-WebRequest -MaximumRedirection 0 -UseBasicParsing `
      -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } `
      -Uri "https://github.com/$Repo/releases/latest" -ErrorAction Stop
    $location = $response.Headers.Location
  } catch {
    if ($_.Exception.Response) {
      $location = $_.Exception.Response.Headers.Location
      if (-not $location) {
        $location = $_.Exception.Response.Headers['Location']
      }
    }
  }

  if ($location) {
    $tag = ([string]$location).TrimEnd('/').Split('/')[-1]
    $candidate = $tag.TrimStart('v')
    if ($candidate -match '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
      return $candidate
    }
  }

  # Fall back to the GitHub API if the redirect path didn't yield a semver tag.
  $release = Invoke-RestMethod -ErrorAction Stop `
    -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } `
    -Uri "https://api.github.com/repos/$Repo/releases/latest"
  if (-not $release.tag_name) {
    Fail 'Could not determine latest release version from GitHub.'
  }

  return $release.tag_name.TrimStart('v')
}

function Download-File([string]$Url, [string]$Destination) {
  # -UseBasicParsing avoids initializing IE's MSHTML parser on Windows
  # PowerShell 5.1 -- required on Server Core and locked-down installs.
  # No-op on PowerShell 6+, where basic parsing is the only mode.
  Invoke-WebRequest -UseBasicParsing -ErrorAction Stop `
    -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } `
    -Uri $Url -OutFile $Destination
}

function Verify-Checksum([string]$ChecksumsPath, [string]$ArchivePath, [string]$ArchiveName) {
  $expected = $null
  foreach ($line in Get-Content $ChecksumsPath) {
    if ($line -match '^(?<hash>[0-9a-fA-F]{64})\s+\*?(?<name>.+)$') {
      if ($Matches.name -eq $ArchiveName) {
        $expected = $Matches.hash.ToLowerInvariant()
        break
      }
    }
  }

  if (-not $expected) {
    Fail "Could not find checksum entry for $ArchiveName"
  }

  $actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLowerInvariant()
  if ($actual -ne $expected) {
    Fail "Checksum verification failed for $ArchiveName"
  }

  Info 'Checksum verified'
}

# Get-CosignBundleSupport decides how to verify the release's Sigstore bundle,
# which is the new (protobuf, v0.3+json) format:
#   v3+          -> new-format parsing is the default; no extra flag ('')
#   v2.6 - v2.x  -> needs '--new-bundle-format=true' (v2.x defaults it to false)
#   < v2.6 / unparseable -> $null: cannot verify (v2.4 chokes on the bundle's
#                  tlog key type, v2.2 lacks the flag); caller warns and skips
function Get-CosignBundleSupport {
  try { $versionOutput = & cosign version 2>$null } catch { return $null }

  foreach ($line in @($versionOutput)) {
    if ("$line" -match 'GitVersion:\s*v?(\d+)\.(\d+)\.') {
      $major = [int]$Matches[1]
      $minor = [int]$Matches[2]
      if ($major -ge 3) { return '' }
      if ($major -eq 2 -and $minor -ge 6) { return '--new-bundle-format=true' }
      return $null
    }
  }

  return $null
}

function Verify-CosignSignature([string]$Version, [string]$BaseUrl, [string]$TmpDir) {
  if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
    return
  }

  $bundleFlag = Get-CosignBundleSupport
  if ($null -eq $bundleFlag) {
    Step "Skipping signature verification: this cosign can't verify the release bundle format (need cosign >= 2.6)"
    return
  }

  Step 'Verifying cosign signature...'

  $bundlePath = Join-Path $TmpDir 'checksums.txt.bundle'
  $checksumsPath = Join-Path $TmpDir 'checksums.txt'
  Download-File -Url "$BaseUrl/checksums.txt.bundle" -Destination $bundlePath

  $cosignArgs = @('verify-blob', '--bundle', $bundlePath)
  if ($bundleFlag) {
    $cosignArgs += $bundleFlag
  }
  $cosignArgs += @(
    '--certificate-identity', "https://github.com/basecamp/basecamp-cli/.github/workflows/release.yml@refs/tags/v$Version",
    '--certificate-oidc-issuer', 'https://token.actions.githubusercontent.com',
    $checksumsPath
  )

  # Native exits don't trigger ErrorActionPreference=Stop on Windows PowerShell 5.1,
  # so check $LASTEXITCODE explicitly -- otherwise a verify failure would false-green.
  & cosign @cosignArgs
  if ($LASTEXITCODE -ne 0) {
    Fail 'Cosign signature verification failed'
  }

  Info 'Signature verified'
}

# Get-FirstRunFailureMessage diagnoses why running the freshly installed
# basecamp.exe failed. It best-effort probes the binary's Authenticode status
# and the Smart App Control state (releases up to v0.8.0-rc.1 ship an unsigned
# basecamp.exe, which Smart App Control blocks at process creation). Every
# branch leads with the original failure: diagnosis augments, never masks,
# the underlying error. PowerShell 5.1-compatible.
function Get-FirstRunFailureMessage([string]$Binary, [string]$Reason) {
  $sigStatus = $null
  try {
    $sigStatus = (Get-AuthenticodeSignature -FilePath $Binary -ErrorAction Stop).Status
  } catch { }

  # Smart App Control state: 0 = off, 1 = on, 2 = evaluation mode.
  $sacState = $null
  try {
    $policy = Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy' -ErrorAction Stop
    $sacState = $policy.VerifiedAndReputablePolicyState
  } catch { }

  $lead = "Installed basecamp.exe to $Binary, but running it failed: $Reason"

  if ("$sigStatus" -eq 'NotSigned' -and ($sacState -eq 1 -or $sacState -eq 2)) {
    return @"
$lead

This build of basecamp.exe is not code-signed, and Smart App Control is enabled. Smart App Control blocks unsigned executables no matter where they were downloaded from, and it has no per-app exceptions. Two options:

  1. (Preferred) Install the Linux build inside WSL2 - Smart App Control does not apply there and your Windows security setup is untouched:
       wsl --install
     then, inside the WSL terminal:
       curl -fsSL https://basecamp.com/install-cli | bash

  2. Turn Smart App Control off (Windows Security > App & browser control > Smart App Control settings) and leave it off while using this unsigned build. Because there are no per-app exceptions, turning it back on re-blocks basecamp.exe on its next run - only re-enable after upgrading to a signed release. Windows 11 with the March/April 2026 updates can re-enable Smart App Control from Windows Security without a reset; on older builds re-enabling requires resetting Windows, so prefer the WSL2 option there.
"@
  }

  if ("$sigStatus" -eq 'NotSigned') {
    return @"
$lead

This build of basecamp.exe is not code-signed, and Windows Security or SmartScreen may have blocked or quarantined it. Check Windows Security > Protection history for a block or quarantine event, restore or allow basecamp.exe, then re-run the installer.
"@
  }

  return @"
$lead

If Windows Security or antivirus interfered, check Windows Security > Protection history, restore or allow basecamp.exe, then re-run the installer.
"@
}

function Get-PathEntries {
  param([string]$PathValue)

  if (-not $PathValue) {
    return @()
  }

  return $PathValue -split ';' | Where-Object { $_ }
}

function Normalize-PathEntry([string]$PathValue) {
  if (-not $PathValue) {
    return ''
  }

  return $PathValue.Trim().TrimEnd('\\')
}

function Get-DefaultBinDir {
  $currentPathEntries = Get-PathEntries $env:Path
  $userPathEntries = Get-PathEntries ([Environment]::GetEnvironmentVariable('Path', 'User'))
  $allEntries = @($currentPathEntries + $userPathEntries) | ForEach-Object { Normalize-PathEntry $_ }

  $homeBin = Normalize-PathEntry (Join-Path $HOME 'bin')
  $homeLocalBin = Normalize-PathEntry (Join-Path $HOME '.local\bin')

  if ($allEntries -contains $homeBin) {
    return $homeBin
  }

  if ($allEntries -contains $homeLocalBin) {
    return $homeLocalBin
  }

  return $homeBin
}

function Ensure-UserPath([string]$Dir) {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $segments = Get-PathEntries $userPath

  $normalizedSegments = $segments | ForEach-Object { Normalize-PathEntry $_ }
  $normalizedDir = Normalize-PathEntry $Dir
  if ($normalizedSegments -contains $normalizedDir) {
    return
  }

  $newPath = if ($userPath) { "$Dir;$userPath" } else { $Dir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  $env:Path = "$Dir;$env:Path"
  Info "Added $Dir to your user PATH"
}

function Test-InteractiveSession {
  if ($Host.Name -ne 'ConsoleHost' -and $Host.Name -ne 'Visual Studio Code Host') {
    return $false
  }

  try {
    return -not [Console]::IsInputRedirected -and -not [Console]::IsOutputRedirected
  } catch {
    return $false
  }
}

# Invoke-FirstTimeSetup runs optional onboarding without changing the successful
# installation result. It returns true when onboarding completes and false after
# printing the command that resumes setup.
function Invoke-FirstTimeSetup([string]$Binary) {
  try {
    & $Binary setup
    if ($LASTEXITCODE -eq 0) {
      return $true
    }
  } catch {
    # The retry guidance below covers process-launch and command failures alike.
  }
  Warn 'First-time setup did not finish. Run it again with: basecamp setup'
  return $false
}

# Invoke-PostInstallSetup installs the baseline skill and connects coding agents
# without prompting, honoring BASECAMP_SETUP_AGENT (claude|codex|all|none;
# unset = auto-detect). It is strictly best-effort: agent setup must never fail
# an otherwise-successful install, so every native call is wrapped so a nonzero
# exit (amplified by $ErrorActionPreference='Stop' +
# $PSNativeCommandUseErrorActionPreference) cannot terminate the installer.
#
# Cross-version: newer binaries expose the intent-neutral `setup agents`. Older
# release binaries (the hosted install.ps1 from main can outrun the latest
# release) fall back WITHOUT reintroducing the Claude-first bug -- only an
# *explicitly* selected agent is connected. `all` runs every per-agent setup the
# binary supports; unset/auto/ambiguous installs the shared skill only. Each
# native call is individually guarded so a nonzero exit never aborts the install.
function Invoke-PostInstallSetup([string]$Binary) {
  # None of these calls touch credentials, but release binaries up to v0.7.2
  # probe the OS keyring on startup for every command, and a locked headless
  # keychain blocks that probe forever. Set the escape hatch for the duration
  # of setup only, restoring the caller's value (or absence) on the way out.
  $savedNoKeyring = $env:BASECAMP_NO_KEYRING
  $env:BASECAMP_NO_KEYRING = '1'
  try {
    try { $help = & $Binary setup --help 2>$null } catch { $help = '' }

    if ($help -match '(?m)^\s+agents\s') {
      try { & $Binary setup agents } catch { }
      return
    }

    $selector = $env:BASECAMP_SETUP_AGENT
    if ($selector -in @('claude', 'codex')) {
      # Capability-check first: an old `setup` parent accepts an unadvertised agent
      # id as a stray arg and launches the INTERACTIVE wizard. Degrade to the skill.
      if ($help -match "(?m)^\s+$selector\s") {
        try { & $Binary setup $selector } catch { }
      } else {
        try { & $Binary skill install } catch { }
      }
    } elseif ($selector -eq 'all') {
      $ranAgent = $false
      foreach ($agent in @('claude', 'codex')) {
        if ($help -match "(?m)^\s+$agent\s") {
          # Mark attempted (not succeeded) -- matches install.sh's `ran_agent=1`,
          # which is set regardless of the setup call's exit status.
          $ranAgent = $true
          try { & $Binary setup $agent } catch { }
        }
      }
      if (-not $ranAgent) { try { & $Binary skill install } catch { } }
    } else {
      try { & $Binary skill install } catch { }
    }
  } finally {
    $env:BASECAMP_NO_KEYRING = $savedNoKeyring
  }
}

function Main {
  $arch = Get-PlatformArch
  if (-not $BinDir) {
    $script:BinDir = Get-DefaultBinDir
  }

  $resolvedVersion = if ($Version) { $Version } else { Get-LatestVersion }

  if ($resolvedVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
    Fail "Invalid version '$resolvedVersion'. Expected semver format like 1.2.3 or 1.2.3-rc.1."
  }

  $archiveName = "basecamp_${resolvedVersion}_windows_${arch}.zip"
  $baseUrl = "https://github.com/$Repo/releases/download/v$resolvedVersion"

  Step "Downloading basecamp v$resolvedVersion for windows_$arch..."
  $tmpDir = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
  New-Item -ItemType Directory -Path $tmpDir | Out-Null

  try {
    $archivePath = Join-Path $tmpDir $archiveName
    $checksumsPath = Join-Path $tmpDir 'checksums.txt'
    $extractDir = Join-Path $tmpDir 'extract'

    Download-File -Url "$baseUrl/$archiveName" -Destination $archivePath

    Step 'Verifying checksums...'
    Download-File -Url "$baseUrl/checksums.txt" -Destination $checksumsPath
    Verify-Checksum -ChecksumsPath $checksumsPath -ArchivePath $archivePath -ArchiveName $archiveName

    Verify-CosignSignature -Version $resolvedVersion -BaseUrl $baseUrl -TmpDir $tmpDir

    Step 'Extracting...'
    Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

    $binaryPath = Join-Path $extractDir 'basecamp.exe'
    if (-not (Test-Path $binaryPath)) {
      Fail 'basecamp.exe not found in archive. If Windows Security or antivirus removed it during extraction, check Windows Security > Protection history, restore it, and re-run the installer.'
    }

    $installedBinary = Join-Path $BinDir 'basecamp.exe'

    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    # Windows holds an exclusive lock on running PE files; -Force doesn't help.
    # Generic catch -- typed catches miss ActionPreferenceStopException wrapping.
    try {
      Copy-Item -Force $binaryPath $installedBinary -ErrorAction Stop
    } catch {
      Fail "Failed to install basecamp.exe. If it is in use, close any running 'basecamp' processes and re-run the installer. If Windows Security quarantined it, check Windows Security > Protection history and restore it. (Original error: $($_.Exception.Message))"
    }
    Ensure-UserPath -Dir $BinDir
    Info "Installed basecamp to $installedBinary"

    # Smart App Control kills CreateProcess for unsigned executables, so the
    # first run is where a block surfaces. Generic catch per the Copy-Item
    # precedent above.
    try {
      $installedVersion = & $installedBinary --version
    } catch {
      Fail (Get-FirstRunFailureMessage -Binary $installedBinary -Reason $_.Exception.Message)
    }
    Info "$installedVersion installed"

    $isInteractive = Test-InteractiveSession

    Write-Host ''
    if ($SkipSetup -eq '1') {
      Step 'Skipping first-time setup (BASECAMP_SKIP_SETUP=1)'
      # Still install the baseline skill and connect coding agents (best-effort).
      Invoke-PostInstallSetup $installedBinary
      Write-Host ''
      Write-Host '  Next steps:'
      Write-Host '    basecamp auth login        Authenticate with Basecamp'
      Write-Host '    basecamp setup             Run first-time setup'
      Write-Host ''
    } elseif ($isInteractive -and -not (Test-TruthyEnvironmentValue $env:BASECAMP_NONINTERACTIVE)) {
      [void](Invoke-FirstTimeSetup $installedBinary)
    } else {
      if (Test-TruthyEnvironmentValue $env:BASECAMP_NONINTERACTIVE) {
        Info 'Skipping first-time setup because BASECAMP_NONINTERACTIVE is enabled.'
      } else {
        Info 'Skipping first-time setup because PowerShell is running non-interactively.'
      }
      # Install the baseline skill and connect coding agents (best-effort).
      Invoke-PostInstallSetup $installedBinary
      Write-Host ''
      Write-Host '  Installed executable:'
      Write-Host "    $installedBinary"
      Write-Host ''
      Write-Host '  In this session, use the installed executable path directly for follow-up actions like starting login.'
      Write-Host ''
    }
  }
  finally {
    if (Test-Path $tmpDir) {
      Remove-Item -Recurse -Force $tmpDir
    }
  }
}

Main

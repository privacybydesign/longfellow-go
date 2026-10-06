# Runs the arm64 harness on a connected Android device and pulls the results.
#
# irmago #724: the two measurements still missing after Phase 0 step 4.
#
#   1. real arm64 timings for OUR stack. The 2.3 s prove / 445 MB figures on
#      record are Multipaz's Kotlin against their prebuilt libzkp.so; nothing of
#      ours has ever run on an arm64 device.
#   2. the first arm64 peak-RSS numbers, PATCHED vs UNPATCHED. memprofile/
#      measured 212 MB -> 148 MB on x86_64 and says in as many words that no
#      arm64 figure exists. This is what would produce one.
#
# Everything pushed here is a plain executable. It needs only liblog, libdl and
# libc, all of which every Android device has -- OpenSSL, zstd and libc++ are
# statically linked in. So there is no APK, no Gradle, no instrumentation source
# set and no patched Multipaz checkout, which is what the earlier on-device
# measurement needed and what cost four failed runs.
#
#   .\run-on-phone.ps1                       # everything
#   .\run-on-phone.ps1 -SkipGoTests          # just the memory probes
#
[CmdletBinding()]
param(
    [string] $CircuitDir = "D:\Yivi\multipaz\multipaz-longfellow\src\commonMain\circuits",
    [string] $BinDir     = (Join-Path $PSScriptRoot "bin"),
    [string] $ResultsDir = (Join-Path $PSScriptRoot "results"),
    [string] $DeviceDir  = "/data/local/tmp/lfbench",
    [switch] $SkipGoTests
)

$ErrorActionPreference = "Stop"

# The v6 one-attribute circuit: what AV readers offer today, and the same one
# every x86_64 figure in memprofile/ was measured against. Comparing like with
# like matters more here than picking the newest.
$Circuit = "6_1_4096_2945_137e5a75ce72735a37c8a72da1a8a0a5df8d13365c2ae3d2c2bd6a0e7197c7c6"

function Assert-Adb {
    if (-not (Get-Command adb -ErrorAction SilentlyContinue)) {
        throw "adb is not on PATH. Install platform-tools, or add them to PATH."
    }
    $devices = @(adb devices | Select-Object -Skip 1 | Where-Object { $_ -match "\sdevice$" })
    if ($devices.Count -eq 0) {
        throw "No device. Connect the phone, enable USB debugging, and accept the RSA prompt on screen."
    }
    if ($devices.Count -gt 1) {
        throw "More than one device is connected; disconnect the others or set ANDROID_SERIAL."
    }
    Write-Host "device: $($devices[0])" -ForegroundColor Green
}

function Push-Harness {
    foreach ($binary in @("mem_probe_baseline", "mem_probe_patched", "longfellow.test")) {
        if (-not (Test-Path (Join-Path $BinDir $binary))) {
            throw "missing $binary in $BinDir -- run scripts/build-android-harness.sh first"
        }
    }
    if (-not (Test-Path (Join-Path $CircuitDir $Circuit))) {
        throw "circuit $Circuit not found in $CircuitDir"
    }

    Write-Host "==> pushing to $DeviceDir" -ForegroundColor Cyan
    adb shell "rm -rf $DeviceDir; mkdir -p $DeviceDir/circuits" | Out-Null

    foreach ($binary in @("mem_probe_baseline", "mem_probe_patched", "longfellow.test")) {
        adb push (Join-Path $BinDir $binary) "$DeviceDir/$binary" | Out-Null
        adb shell "chmod 755 $DeviceDir/$binary" | Out-Null
    }
    # Only the circuits actually used. Pushing all eight would cost a minute of
    # USB for files nothing reads.
    adb push (Join-Path $CircuitDir $Circuit) "$DeviceDir/circuits/$Circuit" | Out-Null

    # Exec from /data/local/tmp is standard for adb, but some vendor ROMs
    # restrict it under SELinux. Finding that out now beats finding it out
    # halfway through a measurement.
    $check = adb shell "cd $DeviceDir && ./mem_probe_baseline 2>&1 | head -1"
    # mem_probe prints usage and exits 2 with no arguments, so a usage line means
    # exec is allowed. Older binaries abort instead; that also proves exec works.
    if ($check -match "Permission denied|not executable|can.t execute") {
        throw "the device refuses to exec from $DeviceDir ($check). This ROM needs the app-based route instead."
    }
    Write-Host "    exec works" -ForegroundColor Green
}

function Get-DeviceFacts {
    Write-Host "==> device" -ForegroundColor Cyan
    $facts = [ordered]@{
        model    = (adb shell getprop ro.product.model).Trim()
        soc      = (adb shell getprop ro.board.platform).Trim()
        android  = (adb shell getprop ro.build.version.release).Trim()
        abi      = (adb shell getprop ro.product.cpu.abi).Trim()
        cores    = (adb shell "cat /proc/cpuinfo | grep -c processor").Trim()
        memTotal = (adb shell "grep MemTotal /proc/meminfo").Trim()
    }
    $facts.GetEnumerator() | ForEach-Object { Write-Host ("    {0,-9} {1}" -f $_.Key, $_.Value) }
    return $facts
}

function Invoke-Probe {
    param([string] $Name)

    Write-Host "==> $Name" -ForegroundColor Cyan
    # Output goes to a file and is pulled. Android discards a native binary's
    # stdout in some contexts and ColorOS has eaten it before; a file is the only
    # reliable channel.
    $command = "cd $DeviceDir && ./$Name circuits/$Circuit 6 1 $DeviceDir/trace_$Name.csv > $DeviceDir/$Name.txt 2>&1; echo exit=`$?"
    $status = adb shell $command
    if ($status -notmatch "exit=0") { Write-Warning "$Name reported $status" }

    adb pull "$DeviceDir/$Name.txt" (Join-Path $ResultsDir "$Name.txt") | Out-Null
    adb pull "$DeviceDir/trace_$Name.csv" (Join-Path $ResultsDir "trace_$Name.csv") | Out-Null
    Get-Content (Join-Path $ResultsDir "$Name.txt") | Where-Object { $_ -match "prove|verify|peak|RSS" }
}

function Invoke-GoTests {
    Write-Host "==> the Go module, on the device" -ForegroundColor Cyan
    # -test.timeout because proving is slow and the default 10m is not generous
    # once every test opens a circuit.
    $command = "cd $DeviceDir && LONGFELLOW_CIRCUITS=$DeviceDir/circuits ./longfellow.test -test.v -test.timeout=30m > $DeviceDir/gotest.txt 2>&1; echo exit=`$?"
    $status = adb shell $command
    adb pull "$DeviceDir/gotest.txt" (Join-Path $ResultsDir "gotest.txt") | Out-Null

    Get-Content (Join-Path $ResultsDir "gotest.txt") |
        Where-Object { $_ -match "^(---|ok|PASS|FAIL)|proved in|verified in|session responded|cold:|warm:" }
    Write-Host "    $status"
}

Assert-Adb
New-Item -ItemType Directory -Force -Path $ResultsDir | Out-Null
$facts = Get-DeviceFacts
$facts | ConvertTo-Json | Set-Content (Join-Path $ResultsDir "device.json")

Push-Harness

# Baseline first, then patched, then the Go tests. The order matters only in
# that a thermally throttled phone flatters whatever ran first -- if the two
# probes disagree by a little, re-run with the order reversed before believing
# it.
Invoke-Probe -Name "mem_probe_baseline"
Invoke-Probe -Name "mem_probe_patched"
if (-not $SkipGoTests) { Invoke-GoTests }

Write-Host ""
Write-Host "==> results in $ResultsDir" -ForegroundColor Green
Write-Host "    device.json, mem_probe_*.txt, trace_*.csv, gotest.txt"
Write-Host "    the phone still holds $DeviceDir; 'adb shell rm -rf $DeviceDir' when done."

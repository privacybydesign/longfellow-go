# Ten cold runs per circuit, baseline vs patched, on a connected device.
#
# "Cold" by construction: every run is a fresh process, so the circuit is read,
# decompressed and parsed again each time and nothing is carried over. That is
# the same methodology as the 2026-09-18 Multipaz measurement, which is what
# makes the two comparable.
#
# What this adds over run-on-phone.ps1: repetition, so the numbers have a spread
# rather than being one sample, and both libraries measured under identical
# conditions.
#
#   .\bench-cold-runs.ps1                 # 10 runs, v6/1 and v7/1
#   .\bench-cold-runs.ps1 -Runs 3         # a quick check
#
[CmdletBinding()]
param(
    [int]    $Runs       = 10,
    [string] $CircuitDir = "D:\Yivi\multipaz\multipaz-longfellow\src\commonMain\circuits",
    [string] $BinDir     = (Join-Path $PSScriptRoot "bin"),
    [string] $ResultsDir = (Join-Path $PSScriptRoot "results"),
    [string] $DeviceDir  = "/data/local/tmp/lfbench"
)

$ErrorActionPreference = "Stop"

# v6/1 and v7/1: the same pair the earlier Multipaz run measured, so the figures
# line up against it.
$Circuits = @(
    [pscustomobject]@{ Label = "v6/1attr"; Version = 6; Attrs = 1
        File = "6_1_4096_2945_137e5a75ce72735a37c8a72da1a8a0a5df8d13365c2ae3d2c2bd6a0e7197c7c6" }
    [pscustomobject]@{ Label = "v7/1attr"; Version = 7; Attrs = 1
        File = "7_1_4151_4096_8d079211715200ff06c5109639245502bfe94aa869908d31176aae4016182121" }
)
$Binaries = @("mem_probe_baseline", "mem_probe_patched")

function Assert-Device {
    $devices = @(adb devices | Select-Object -Skip 1 | Where-Object { $_ -match "\sdevice$" })
    if ($devices.Count -ne 1) { throw "need exactly one connected device; found $($devices.Count)" }
    Write-Host "device: $($devices[0])" -ForegroundColor Green
}

function Push-Everything {
    Write-Host "==> pushing" -ForegroundColor Cyan
    adb shell "mkdir -p $DeviceDir/circuits" | Out-Null
    foreach ($binary in $Binaries) {
        $local = Join-Path $BinDir $binary
        if (-not (Test-Path $local)) { throw "missing $local -- run scripts/build-android-harness.sh" }
        adb push $local "$DeviceDir/$binary" | Out-Null
        adb shell "chmod 755 $DeviceDir/$binary" | Out-Null
    }
    foreach ($circuit in $Circuits) {
        $local = Join-Path $CircuitDir $circuit.File
        if (-not (Test-Path $local)) { throw "missing circuit $($circuit.File) in $CircuitDir" }
        adb push $local "$DeviceDir/circuits/$($circuit.File)" | Out-Null
    }
}

# Parses one run's output. The probe prints its own timings; nothing here is
# measured from the host side, where USB latency would be in the numbers.
function Read-Run {
    param([string[]] $Output)

    $result = [ordered]@{ ProveMs = $null; VerifyMs = $null; PeakKb = $null; ProofBytes = $null }
    foreach ($line in $Output) {
        if ($line -match "prove:\s+(\d+)\s+byte proof in\s+(\d+)\s+ms") {
            $result.ProofBytes = [int]$Matches[1]; $result.ProveMs = [int]$Matches[2]
        }
        elseif ($line -match "verify:\s+OK in\s+(\d+)\s+ms") { $result.VerifyMs = [int]$Matches[1] }
        elseif ($line -match "peak sampled RSS:\s+(\d+)\s+kB")  { $result.PeakKb  = [int]$Matches[1] }
    }
    return $result
}

Assert-Device
New-Item -ItemType Directory -Force -Path $ResultsDir | Out-Null
Push-Everything

$rows = New-Object System.Collections.Generic.List[object]

foreach ($circuit in $Circuits) {
    foreach ($binary in $Binaries) {
        $variant = if ($binary -match "patched") { "patched" } else { "baseline" }
        Write-Host ("==> {0}  {1}  x{2}" -f $circuit.Label, $variant, $Runs) -ForegroundColor Cyan

        for ($i = 1; $i -le $Runs; $i++) {
            # No trace CSV per run: writing 300 samples to storage 40 times would
            # put the device's own I/O into the measurement.
            $command = "cd $DeviceDir && ./$binary circuits/$($circuit.File) $($circuit.Version) $($circuit.Attrs) 2>/dev/null"
            $output  = adb shell $command
            $parsed  = Read-Run -Output $output

            if ($null -eq $parsed.ProveMs) { Write-Warning "run $i produced no timings"; continue }

            $rows.Add([pscustomobject]@{
                Circuit = $circuit.Label; Variant = $variant; Run = $i
                ProveMs = $parsed.ProveMs; VerifyMs = $parsed.VerifyMs
                PeakKb  = $parsed.PeakKb;  PeakMb = [math]::Round($parsed.PeakKb / 1024.0, 1)
                ProofBytes = $parsed.ProofBytes
            })
            Write-Host ("    run {0,2}: prove {1,5} ms  verify {2,4} ms  peak {3,6:N1} MB" -f `
                $i, $parsed.ProveMs, $parsed.VerifyMs, ($parsed.PeakKb / 1024.0))
        }
    }
}

$csv = Join-Path $ResultsDir "cold-runs.csv"
$rows | Export-Csv -Path $csv -NoTypeInformation
Write-Host ""
Write-Host "==> $($rows.Count) runs -> $csv" -ForegroundColor Green

# Median rather than mean: one thermally throttled outlier should not move the
# headline figure, and with ten samples the median is the honest summary.
function Get-Median {
    param([double[]] $Values)
    $sorted = $Values | Sort-Object
    $n = $sorted.Count
    if ($n -eq 0) { return $null }
    if ($n % 2) { return $sorted[[math]::Floor($n / 2)] }
    return ($sorted[$n / 2 - 1] + $sorted[$n / 2]) / 2
}

Write-Host ""
Write-Host ("{0,-10} {1,-9} {2,10} {3,10} {4,10}" -f "circuit", "variant", "prove", "verify", "peak")
$summary = foreach ($group in $rows | Group-Object Circuit, Variant) {
    $r = $group.Group
    $row = [pscustomobject]@{
        Circuit     = $r[0].Circuit
        Variant     = $r[0].Variant
        Runs        = $r.Count
        ProveMedian = [int](Get-Median ($r.ProveMs))
        ProveMin    = ($r.ProveMs | Measure-Object -Minimum).Minimum
        ProveMax    = ($r.ProveMs | Measure-Object -Maximum).Maximum
        VerifyMedian= [int](Get-Median ($r.VerifyMs))
        VerifyMin   = ($r.VerifyMs | Measure-Object -Minimum).Minimum
        VerifyMax   = ($r.VerifyMs | Measure-Object -Maximum).Maximum
        PeakMbMedian= [math]::Round((Get-Median ($r.PeakKb)) / 1024.0, 1)
    }
    Write-Host ("{0,-10} {1,-9} {2,7} ms {3,7} ms {4,7:N1} MB" -f `
        $row.Circuit, $row.Variant, $row.ProveMedian, $row.VerifyMedian, $row.PeakMbMedian)
    $row
}

$summaryPath = Join-Path $ResultsDir "cold-runs-summary.json"
$summary | ConvertTo-Json -Depth 4 | Set-Content $summaryPath
Write-Host ""
Write-Host "==> summary -> $summaryPath" -ForegroundColor Green

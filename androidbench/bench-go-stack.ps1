# Ten cold runs of OUR FULL STACK on a connected device.
#
# Why this exists alongside bench-cold-runs.ps1: that script measures a bare C++
# probe, which is the right instrument for comparing two builds of the library
# against each other. It is the wrong instrument for comparing against Multipaz,
# whose published figures come from their full Kotlin/JNI harness.
#
# The like-for-like counterpart is the Go module: irmago's adapter, our cgo
# binding, and the library, driven by the same test a wallet's code path would
# take. That is what this measures.
#
# Cold by construction: one fresh process per run, and the circuit directory
# holds exactly one circuit so the prover cannot pick a different one.
#
#   .\bench-go-stack.ps1              # 10 runs, v6 and v7
#   .\bench-go-stack.ps1 -Runs 3
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

$Circuits = @(
    [pscustomobject]@{ Label = "v6/1attr"
        File = "6_1_4096_2945_137e5a75ce72735a37c8a72da1a8a0a5df8d13365c2ae3d2c2bd6a0e7197c7c6" }
    [pscustomobject]@{ Label = "v7/1attr"
        File = "7_1_4151_4096_8d079211715200ff06c5109639245502bfe94aa869908d31176aae4016182121" }
)

# One test, not the suite. The suite opens circuits for every case and would put
# twenty circuit loads into a measurement of one prove.
$Test = "TestProveAndVerifyRoundTripThroughTheAdapter"

$devices = @(adb devices | Select-Object -Skip 1 | Where-Object { $_ -match "\sdevice$" })
if ($devices.Count -ne 1) { throw "need exactly one connected device; found $($devices.Count)" }
Write-Host "device: $($devices[0])" -ForegroundColor Green

adb push (Join-Path $BinDir "longfellow.test") "$DeviceDir/longfellow.test" | Out-Null
adb shell "chmod 755 $DeviceDir/longfellow.test" | Out-Null

# A directory per circuit. MatchingSpec prefers the newest version it holds, so
# a directory with both would answer every run with v7 and the v6 column would
# silently be a second v7 column.
foreach ($c in $Circuits) {
    $dir = "$DeviceDir/only_$($c.Label -replace '[/ ]','')"
    adb shell "rm -rf $dir; mkdir -p $dir" | Out-Null
    adb push (Join-Path $CircuitDir $c.File) "$dir/$($c.File)" | Out-Null
}

$rows = New-Object System.Collections.Generic.List[object]
New-Item -ItemType Directory -Force -Path $ResultsDir | Out-Null

foreach ($c in $Circuits) {
    $dir = "$DeviceDir/only_$($c.Label -replace '[/ ]','')"
    Write-Host ("==> {0}  our Go stack  x{1}" -f $c.Label, $Runs) -ForegroundColor Cyan

    for ($i = 1; $i -le $Runs; $i++) {
        $cmd = "cd $DeviceDir && LONGFELLOW_CIRCUITS=$dir ./longfellow.test -test.run '^$Test`$' -test.v 2>/dev/null"
        $out = adb shell $cmd

        # The test logs "proved in 1.403s, N byte proof, circuit ..." and
        # "verified in 735ms". Go's duration formatting switches units, so both
        # seconds and milliseconds have to be accepted.
        $prove = $null; $verify = $null; $bytes = $null
        foreach ($line in $out) {
            if ($line -match "proved in ([0-9.]+)(ms|s), (\d+) byte proof") {
                $prove = [double]$Matches[1]; if ($Matches[2] -eq "s") { $prove *= 1000 }
                $bytes = [int]$Matches[3]
            }
            elseif ($line -match "verified in ([0-9.]+)(ms|s)") {
                $verify = [double]$Matches[1]; if ($Matches[2] -eq "s") { $verify *= 1000 }
            }
        }
        if ($null -eq $prove) { Write-Warning "run $i produced no timings"; continue }

        $rows.Add([pscustomobject]@{
            Circuit = $c.Label; Stack = "ours (Go)"; Run = $i
            ProveMs = [int]$prove; VerifyMs = [int]$verify; ProofBytes = $bytes
        })
        Write-Host ("    run {0,2}: prove {1,5} ms  verify {2,4} ms  proof {3} B" -f `
            $i, [int]$prove, [int]$verify, $bytes)
    }
}

$csv = Join-Path $ResultsDir "go-stack-cold-runs.csv"
$rows | Export-Csv -Path $csv -NoTypeInformation

function Get-Median { param([double[]]$v) $s=$v|Sort-Object; $n=$s.Count; if($n%2){$s[[math]::Floor($n/2)]}else{($s[$n/2-1]+$s[$n/2])/2} }

Write-Host ""
foreach ($g in $rows | Group-Object Circuit) {
    $r = $g.Group
    Write-Host ("{0,-10} prove {1,5} ms ({2}-{3})   verify {4,4} ms ({5}-{6})" -f `
        $r[0].Circuit,
        [int](Get-Median ([double[]]$r.ProveMs)),  ($r.ProveMs|Measure-Object -Minimum).Minimum,  ($r.ProveMs|Measure-Object -Maximum).Maximum,
        [int](Get-Median ([double[]]$r.VerifyMs)), ($r.VerifyMs|Measure-Object -Minimum).Minimum, ($r.VerifyMs|Measure-Object -Maximum).Maximum)
}
Write-Host ""
Write-Host "==> $($rows.Count) runs -> $csv" -ForegroundColor Green

# Reproducing WORM benchmarks

The committed evidence is in [`results/`](results/): [1,000-event pressure smoke](results/pressure_smoke.txt), [durable pipeline worker scaling](results/pressure_scaling.txt), [decoder/pack/normalizer microbenchmarks](results/microbenchmarks.txt), and the [10,000-event all-format pressure run](results/pressure_rigorous_10k.txt). These were captured on an Apple M2 with 16 GB RAM running Darwin arm64 on 2026-09-24. The 1,000-event smoke run reported 4,623.1 events/s, p50 166 µs, p99 1.542 ms; the concurrent 10,000-event all-format run reported 10,000 normalized, zero quarantined/pending, 22.096 s (452.6 events/s), and queue-inclusive p99 21.691 s. These measure **different workloads**; neither proves sustained or multi-node capacity.

To reproduce on your machine, use Go 1.25+ from a checkout, keep your machine otherwise idle, record `git rev-parse HEAD`, `git status --short`, `go version`, OS/CPU/RAM/storage, active packs/sinks, start/end UTC, and the exact commands. Save new logs under ignored `data/benchmarks/` so the published evidence is not overwritten. The benchmark harness builds its own temporary store; live methods must use a fresh `-db` path for each run. Report accepted, normalized, quarantined, pending, producer acknowledgements, output counts, and errors together with elapsed time. `accepted = normalized + quarantined + pending` is lifecycle accounting, not exactly-once external delivery.

## Method A — concurrent all-format pipeline (published 10k result)

This Go test submits seven format families from eight producers to a durable SQLite pipeline, drains it, and records queue-inclusive latency. It does **not** include a network listener.

PowerShell:

```powershell
New-Item -ItemType Directory -Force data/benchmarks | Out-Null
$env:WORM_RIGOROUS_BENCH = '1'
$env:WORM_BENCH_EVENTS = '10000'
go test -count=1 -timeout=10m -v -run '^TestRigorousPressureMatrix$' ./test/pressure 2>&1 | Tee-Object data/benchmarks/rigorous-10k.txt
Remove-Item Env:WORM_RIGOROUS_BENCH, Env:WORM_BENCH_EVENTS
```

Bash:

```sh
mkdir -p data/benchmarks
WORM_RIGOROUS_BENCH=1 WORM_BENCH_EVENTS=10000 go test -count=1 -timeout=10m -v -run '^TestRigorousPressureMatrix$' ./test/pressure 2>&1 | tee data/benchmarks/rigorous-10k.txt
```

Pass if `events=10000`, `normalized=10000`, `quarantined=0`, `pending=0`, and `PASS` appear. The throughput is a saturated single-store run, not an EPS promise.

## Method B — fixed-work Go benchmark

This compares worker configurations and allocation cost. `-benchtime=10000x` runs **10,000 iterations per sub-benchmark**, not one shared 10,000-event run. The committed scaling result used Go's normal benchmark calibration, so its exact `ns/op` need not match a fixed-work rerun.

```sh
go test -count=1 -run '^$' -bench '^BenchmarkPipeline_E2E$' -benchtime=10000x -benchmem ./test/pressure
```

For the decoder/pack measurements in `results/microbenchmarks.txt`:

```sh
go test -run '^$' -bench . -benchmem ./internal/decode ./internal/packs ./internal/normalize
```

## Method C — CLI over stdin

This includes the simulator, OS pipe, CLI framing, SQLite, and NDJSON output. It is a single-producer correctness run, not maximum throughput. In PowerShell from the repository root:

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
go build -o bin/worm.exe ./cmd/worm
go build -o bin/logsim.exe ./cmd/logsim
$run = "data/benchmarks/stdin-$(Get-Date -Format 'yyyyMMdd-HHmmss')"
New-Item -ItemType Directory -Force "$run/sources", "$run/sinks" | Out-Null
$elapsed = Measure-Command {
  .\bin\logsim.exe --source app --scenario normal --format stdout --rate 0 --count 10000 |
    .\bin\worm.exe -stdin -stdin-framing line -db "$run/worm.db" -packs-dir packs -sources-dir "$run/sources" -sinks-dir "$run/sinks" -workers 4 -syslog-udp none -syslog-tcp none -syslog-tls none -http none -ui none -inbox none -output-file "$run/normalized.ndjson" -stdout=false 2>&1 |
    Tee-Object "$run/worm.txt" | Out-Host
}
"elapsed_seconds=$($elapsed.TotalSeconds)" | Set-Content "$run/duration.txt"
Select-String -Path "$run/worm.txt" -Pattern 'Accepted:|Normalized:|Quarantined:|Pending:|Loss Audit:'
```

Check `Accepted: 10000`, `Normalized: 10000`, `Quarantined: 0`, `Pending: 0`, and a passing loss audit. This method has no committed 10k result; do not attribute the Method A measurement to it.

## Method D — live HTTP intake, 10,000 events

This exercises a real listener and HTTP acknowledgements. The simulator sends sequential app JSON requests; `acked=10000` is the producer's count of successful responses, not proof that downstream sinks committed 10,000 records. First build `bin/worm.exe` and `bin/logsim.exe` as shown in Method C. Use two PowerShell terminals and a **new** run directory each time.

Terminal 1:

```powershell
$run = 'data/benchmarks/http-10k-01'
New-Item -ItemType Directory -Force "$run/sources", "$run/sinks" | Out-Null
.\bin\worm.exe -db "$run/worm.db" -packs-dir packs -sources-dir "$run/sources" -sinks-dir "$run/sinks" -workers 4 -syslog-udp none -syslog-tcp none -syslog-tls none -http 127.0.0.1:18080 -ui 127.0.0.1:19090 -inbox none -output-file "$run/normalized.ndjson" -stdout=false 2>&1 | Tee-Object "$run/worm.txt"
```

Terminal 2:

```powershell
$run = 'data/benchmarks/http-10k-01'
$elapsed = Measure-Command {
  .\bin\logsim.exe --source app --scenario normal --format http-post --target http://127.0.0.1:18080/api/v1/ingest --rate 0 --count 10000 2>&1 |
    Tee-Object "$run/sender.txt" | Out-Host
}
"sender_elapsed_seconds=$($elapsed.TotalSeconds)" | Set-Content "$run/duration.txt"
do {
  $stats = Invoke-RestMethod http://127.0.0.1:19090/api/v1/stats
  if ($stats.pending -ne 0) { Start-Sleep -Seconds 1 }
} while ($stats.pending -ne 0)
$stats | Select-Object accepted,normalized,quarantined,pending,delivered,delivery_pending,delivery_failed
Select-String -Path "$run/sender.txt" -Pattern 'Finished logsim:'
```

Pass only if the sender reports `emitted=10000 acked=10000 errors=0` and the final API reports `accepted=10000 normalized=10000 quarantined=0 pending=0`. Stop WORM with Ctrl+C after retaining the stats. The sender's elapsed time excludes subsequent drain; publish both sender time and final-drain time if you report complete-path latency. No Method D 10k result is committed today.

For Linux/macOS, build `bin/worm` and `bin/logsim` without `.exe`, use shell variables and `tee`, and query the stats endpoint with `curl`. Keep Method A–D results separate: they cross different boundaries and cannot be ranked by their EPS alone. A 10,000-event burst is not a sustained one-billion-events/day validation; that requires a measured multi-node, real-broker/sink soak with bounded backlog and recovery evidence.

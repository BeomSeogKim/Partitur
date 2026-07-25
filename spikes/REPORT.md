# DESIGN v0.2 bounded-spike report

Date: 2026-07-25

Normative input: `docs/DESIGN.md` at DESIGN v0.2. This report does not amend it.

## Executive verdict

| Spike | Verdict |
|---|---|
| A.1 canonical encoding | **Survives with amendments.** JCS is implementable in Go and the numeric/schema split is sound. The YAML “rejected at parse time” wording is not literally achievable with the evaluated general-purpose parser, YAML 1.2 sexagesimal is a string rather than an implicit numeric tag, and negative zero needs an explicit ingress policy. |
| §5 fan-in composition | **The merge loop survives; §5 must change.** `git merge-tree --write-tree` is the correct checkout-free primitive on supported Git, but the exact invocation, `GIT_ATTR_SOURCE`, minimum Git version, merge options/config, and custom-driver policy are identity dependencies. Git version alone is insufficient. |
| §6 lease and wake-up | **Survives with an amendment.** PID plus process-start identity works, and journal polling interrupts a blocked stdout reader. Fencing only works if every mutation checks the incarnation token and a monotonic authority epoch under the state lock. |
| §4 process supervision | **Must change.** A new adapter session plus repeated session sweep cleans a conforming adapter→vendor chain on macOS and Linux, including separate process groups. It is not absolute portable containment: a descendant can call `setsid()` and leave the outer session’s selectable set. |

## Reproduction

Host:

```text
macOS 26.5.2 arm64
Go 1.26.5
Apple Git 2.50.1 (Apple Git-155)
Docker 29.5.2, Linux/arm64
```

Commands:

```sh
cd spikes
GOCACHE=/private/tmp/partitur-go-cache go test -v ./canonical
GOCACHE=/private/tmp/partitur-go-cache go test -v ./fanin
GOCACHE=/private/tmp/partitur-go-cache go test -v ./supervision

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  GOCACHE=/private/tmp/partitur-go-cache \
  go test -c -o /private/tmp/fanin-linux-arm64.test ./fanin
docker run --rm \
  -v /private/tmp/fanin-linux-arm64.test:/fanin.test:ro \
  alpine:3.21 sh -c 'apk add --no-cache git >/dev/null && /fanin.test -test.v'
docker run --rm \
  -v /private/tmp/fanin-linux-arm64.test:/fanin.test:ro \
  alpine:3.22 sh -c 'apk add --no-cache git >/dev/null && /fanin.test -test.v'

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  GOCACHE=/private/tmp/partitur-go-cache \
  go test -c -o /private/tmp/supervision-linux-arm64.test ./supervision
docker run --rm \
  -v /private/tmp/supervision-linux-arm64.test:/supervision.test:ro \
  alpine:3.22 /supervision.test -test.v
```

The spike is a nested Go module so its YAML dependency does not alter the production module.

## Spike 1 — canonical encoding and numeric range

### Result

The encoder in `canonical/jcs.go` accepts a validated JSON AST and implements:

- recursive object-key sorting by unsigned UTF-16 code units;
- no Unicode normalization;
- JCS string escaping;
- finite binary64 number serialization with ECMAScript exponent boundaries;
- `-0` serialization as `0` at the encoder layer, as RFC 8785 specifies.

Conformance results:

| Corpus | Result |
|---|---|
| RFC 8785 Appendix B number table, including ±0, min subnormal, max finite, exponent boundaries, and round-to-even | PASS |
| Published deterministic ES6 number file, first 1,000 records | PASS; SHA-256 `be18b62b6f69cdab33a7e0dae0d9cfa869fda80ddc712221570f9f40a5878687` exactly |
| RFC 8785 canonicalization example | PASS byte-for-byte |
| RFC-linked `cyberphone/json-canonicalization` published canonicalization vector (upstream commit `19d51d7fe467d4706a3ff08adf8a748f29fc21e0`) | PASS. Only this vector is pinned in the committed reproduction; the wider upstream file-vector set was exercised during the spike but is not reproducible from this repository. |

The RFC-linked implementation is useful evidence but is not a complete Partitur ingress layer by itself: its filter interface does not supply Partitur’s duplicate-key, YAML, and schema-boundary validation.

### Numeric split

One encoder is enough. The split belongs before encoding:

1. Decode every JSON number to a finite IEEE-754 binary64 value.
2. Apply schema validation only at schema-controlled paths: integral and in `[-(2^53-1), 2^53-1]`.
3. Leave opaque extension values as finite binary64 numbers, including fractions and magnitudes outside the safe-integer range.
4. Feed both to the same JCS encoder.

The following are rejected at ingress rather than encoded:

- duplicate object names;
- invalid UTF-8 and lone UTF-16 surrogates;
- NaN and ±Infinity in programmatic values;
- overflow such as `1e9999`;
- non-zero values that underflow to binary64 zero, such as `1e-9999`;
- negative-zero JSON/YAML spellings under the chosen strict identity policy.

JCS itself maps negative zero to `0`. Verified RFC 8785 erratum 7920 says parsers should reject `-0` to avoid ambiguity. Partitur identities should follow that stricter ingress rule; otherwise `-0` and `0` silently share an identity.

Decimal lexical precision is not preserved. That is intentional JCS behavior: the parsed binary64 value is canonicalized. A value needing decimal or integer precision beyond binary64 must be represented as a string.

### YAML corpus

Evaluated library: `go.yaml.in/yaml/v4 v4.0.0-rc.6`.

| Case | Library/wrapper result |
|---|---|
| Literal/folded block scalar, clip (`|`, `>`) | final newline preserved |
| Strip (`|-`) | final newline removed |
| Keep (`|+`) | additional final newlines preserved |
| Quoted timestamp | string, accepted |
| Implicit timestamp | resolves to `!!timestamp`, wrapper rejects |
| Explicit `!!binary` and custom tag | parsed as tagged nodes, wrapper rejects |
| YAML 1.2 `yes/no/on/off` | strings, accepted |
| Sexagesimal-looking `12:34:56` | YAML 1.2 string; custom lexical rejection is required |
| Duplicate keys, anchors, aliases, merge keys | parser can represent them; wrapper traversal rejects |
| `.nan`, `.inf`, negative zero | wrapper rejects |

A general YAML library first builds a YAML representation graph. Rejection “at parse time” is achievable only at the `yamlsafe` API boundary by validating that graph before constructing the canonical AST; it is not generally an error from the underlying parser.

### Unicode ordering

The difference is observable:

```text
keys: U+E000, U+10000
Go UTF-8/string order: U+E000, U+10000
JCS UTF-16 order:      U+10000, U+E000
```

U+10000 starts with UTF-16 unit `D800`, which sorts before `E000`. Naive Go string sorting silently produces a different identity.

Composed `Å` and decomposed `A + U+030A` also produced different canonical bytes, confirming no normalization.

### Required A.1 replacement

Replace:

> Timestamp, binary, sexagesimal, and every other implicit tag is rejected at parse time.

with:

> `yamlsafe` parses one YAML 1.2 representation graph, rejects duplicate keys, anchors, aliases, merge keys, custom tags, and every resolved scalar tag other than `!!str`, `!!bool`, `!!null`, `!!int`, or `!!float`, validates numeric scalars as finite representable binary64 values, and only then constructs the JSON AST. YAML 1.2 resolves sexagesimal-looking plain scalars as strings, so `yamlsafe` rejects that lexical form explicitly. These are `yamlsafe` decode errors; the underlying YAML parser need not reject them while constructing its representation graph.

Add:

> Raw JSON and YAML ingress rejects negative-zero spellings, non-zero numbers that underflow to zero, overflow, NaN, and Infinity before canonicalization. The encoder never receives a non-finite number; programmatic negative zero is serialized as `0` per RFC 8785.

## Spike 2 — fan-in tree composition

### Exact plumbing

For each change set:

```sh
GIT_CONFIG_NOSYSTEM=1 \
GIT_CONFIG_SYSTEM=/dev/null \
GIT_CONFIG_GLOBAL=/dev/null \
GIT_ATTR_SOURCE=<ours-tree> \
git --git-dir=<git-dir> --work-tree=<work-tree> \
  merge-tree --write-tree \
  --merge-base=<change-set-base-tree> \
  --name-only -z --no-messages \
  <current-composed-tree> <change-set-result-tree>
```

Interpretation:

- exit `0`: first NUL-delimited field is the result tree;
- exit `1`: first field is Git’s conflict tree and the remaining fields are exact conflicted paths;
- any other exit: infrastructure failure, not a composition conflict.

This invocation did not create or alter an index, did not change HEAD, and left the experiment worktree empty. `git read-tree -m` is not sufficient: it cannot provide the modern content merge, rename handling, or conflict evidence required here.

`GIT_ATTR_SOURCE=<ours-tree>` is required so attributes are read from the composed “ours” tree rather than an unrelated checkout. A bare repository plus this environment failed on Apple Git 2.50.1 with:

```text
BUG: attr.c:685: non-INDEX attr direction in a bare repo
```

Use a non-bare temporary repository with an empty worktree. Do not use a bare temporary repository for this invocation, and — per the custom-driver finding below — do not use the source repository either: its local config and `$GIT_DIR/info/attributes` would be consulted.

### Matrix

| Case | Result |
|---|---|
| rename/rename | conflict; Git reported old and both destination paths |
| rename/delete | conflict; renamed destination reported |
| add/add | conflict |
| file mode on one side + content on the other | clean; executable bit and content both preserved |
| symlink target changed on both sides | conflict |
| symlink ↔ regular file | conflict; Git also emitted an auxiliary conflict path |
| submodule fast-forward | clean; advanced gitlink selected |
| divergent submodule commits | conflict |
| `text eol=lf`, default `merge.renormalize=false` | clean; stored CRLF retained |
| same input with `merge.renormalize=true` | clean; result normalized to LF and tree changed |
| `-merge` | conflict |
| NUL-containing binary modified on both sides | conflict |
| configured custom driver `cp %B %A` | clean; selected theirs |
| same trees, driver changed to `true` | clean; retained ours; different result tree |
| no-op `merge(base=B, ours=T, theirs=B)` | clean identity; result exactly `T` |
| each change set merged against its own base | clean and produced expected final tree |
| duplicate `change_set_id` | skipped; applied sequence contained it once |
| conflict path containing a newline | recovered exactly through NUL-delimited output |

### OS and Git versions

The complete matrix produced identical tree OIDs and conflicted-path sets on:

- Linux/arm64 Git 2.47.3;
- Linux/arm64 Git 2.49.1;
- macOS/arm64 Apple Git 2.50.1.

Alpine 3.19 Git 2.43.7 returned success with empty output for the explicit tree-input invocation. Testing both NUL and legacy newline output and toggling messages did not change that result. The precise first compatible version between 2.43.7 and 2.47.3 was not bisected.

The version dependency is therefore load-bearing even though the three supported versions produced identical trees. More importantly, the custom-driver experiment proves Git version alone is not enough: repository config and the executable behind the driver can change the tree for identical base/ours/theirs trees.

### Required §5 replacements

Add the exact invocation:

> v0.2 composes each step with `git merge-tree --write-tree --merge-base=<C.base_tree> --name-only -z --no-messages <T> <C.result_tree>` in a non-bare repository, with system/global Git config isolated and `GIT_ATTR_SOURCE=<T>`. Exit 0 yields the result tree; exit 1 yields NUL-delimited conflicted paths; every other exit is an infrastructure failure. The supported Git floor is 2.47 until a narrower compatibility floor is separately proved.

Replace the version-only dependency sentence with:

> The composition dependency records the exact Git build, object format, merge invocation and strategy options, `merge.renormalize`, and every applicable repository merge-config input. v0.2 either rejects external custom merge drivers or records a pinned content identity for each driver implementation and its configuration; recording Git version alone is insufficient.

The simpler and safer v0.2 choice is to reject external custom merge drivers. Otherwise composition can execute arbitrary repository-configured commands and depends on executables outside the input trees.

## Spike 3 — lease, wake-up, and process supervision

### Portable process-start identity

Linux:

```text
/proc/<pid>/stat field 22 starttime
+ /proc/sys/kernel/random/boot_id
```

Field 22 is clock ticks since boot. The boot ID is required because a repository lease can survive reboot; PID plus start ticks alone can collide across boots. Both files are readable in ordinary Go without cgo. `/proc/<pid>/stat` must be parsed after the final `)` because the command name may contain spaces and `)` characters.

macOS:

```text
proc_pidinfo(pid, PROC_PIDTBSDINFO, ...).pbi_start_tvsec
proc_pidinfo(pid, PROC_PIDTBSDINFO, ...).pbi_start_tvusec
```

The spike used the public `proc_info` syscall (`SYS_PROC_INFO`) directly with the SDK’s `proc_bsdinfo` layout and no cgo. It successfully re-read the current and child-process identities on macOS 26.5.2. `golang.org/x/sys/unix` also exposes the sysctl/KinfoProc path without application cgo, but `proc_pidinfo` is smaller and directly exposes the start timeval needed here.

Cross-user or sandbox policy can deny process inspection. Failure to read or re-verify identity must be treated as “owner not safely verifiable”, never as proof that the recorded owner is live.

### Lease and fencing

The spike passed:

- stale lease after holder `SIGKILL`, reclaimed with a higher epoch;
- PID-reuse condition, simulated exactly as a currently live PID with the old start identity;
- two simultaneous OS-process acquisition attempts, exactly one winner;
- stopped owner, authority fence, `SIGCONT`, attempted mutation: mutation rejected and no bytes written.

The lease file contains PID, process-start identity, random incarnation token, and authority epoch. Acquisition/reclamation runs under the state lock. Every mutation takes the same lock and checks:

```text
run is nonterminal
AND authority.epoch == owner.epoch
AND authority.token == owner.token
AND driver.lease has the same epoch and token
AND PID/start identity still matches
```

The fence increments the authority epoch, clears the current token, marks the run terminal, and removes the lease while holding the state lock. A resumed old process therefore cannot mutate even if it retained its old token in memory.

### Mid-execute wake-up

The adapter printed one ready line and then blocked forever without closing stdout. The driver’s stdout scanner remained blocked in one goroutine while a second goroutine tailed the authoritative journal every 20 ms.

Observed journal-append-to-detection latency:

| Platform | Latency |
|---|---:|
| macOS | 22.3 ms |
| Linux | 23.8 ms |

The mechanism is portable and bounded by poll interval plus scheduling/I/O latency. A signal can prompt an immediate read, but correctness does not depend on it. Production should parse complete JSONL records and keep the last complete offset; the spike uses the event substring only to isolate wake-up behavior.

### Process supervision

Note on enumeration scope, added after review: the Darwin enumerator lists only the current uid's
processes (`PROC_UID_ONLY`) rather than all PIDs. macOS has no `PROC_SESSION_ONLY`, and inspecting
another user's process returns `EPERM`, so an enumerate-all-then-inspect strategy cannot be made
fail-closed without aborting on every ordinary system. A session the core created contains only
processes it spawned under its own uid, so the narrower listing answers the same question while
making every listed PID inspectable — which is what lets an inspection failure be treated as
alarming rather than routine.

Working portable mechanism for conforming adapters:

1. Core starts the adapter as a new POSIX session leader (`Setsid: true`).
2. Adapter may start the vendor in a separate process group (`Setpgid: true`) but not a separate session.
3. On termination, core enumerates processes, selects the adapter session by SID, sends TERM to every group and verified PID, waits the outer grace, then repeatedly sends KILL and re-enumerates until no live session member remains.
4. PID/start identity is checked before individual signals to reduce PID-reuse races.

The demonstrated wedged adapter and vendor used different process groups but one session. After sweep, live survivors were zero on macOS and Linux.

This is not absolute containment. A descendant that calls `setsid()` received a new SID and was absent from the outer session’s selected members on both platforms. Linux cgroup v2 can provide a non-escapable ownership set if Partitur has permission to create/use one. macOS has no cgroup equivalent; process enumeration cannot close the race after a child daemonizes, reparents, and loses its ancestry/session relationship.

### Required §6/§4 replacements

Add to §6:

> The process-start identity is Linux boot ID plus `/proc/<pid>/stat` field 22, or macOS `PROC_PIDTBSDINFO` start seconds and microseconds. Failure to read or re-verify it fails closed. The random incarnation token is also a fencing token: every **driver-authorized** mutation — the ones that constitute execution — must, while holding the repository state lock, CAS the current nonterminal authority epoch and token against an existing matching lease. Commands that mutate without holding the lease (`answer`, approvals, `amend`, `cancel`, `apply`, `promote-score`) authorize against run lifecycle instead. Fencing and terminalization are a **single** lock-held transition authorized by run lifecycle, because after the epoch moves neither the canceller nor the fenced driver holds driver authority.

Replace the portable absolute-cleanup implication in §4 with:

> Core starts each adapter as a new POSIX session. A conforming adapter may create vendor process groups but MUST NOT create or permit vendor descendants to create another session. Outer termination repeatedly enumerates and terminates every verified member of the adapter session after the adapter grace period. This is portable cleanup for conforming descendants, not an OS containment boundary: Linux uses cgroup v2 when available for an absolute ownership set; macOS requires a future containment backend. If “no descendant survives” is an absolute enforcement requirement rather than an adapter conformance requirement, v0.2 must fail closed on macOS because POSIX process/session primitives cannot guarantee it.

## Not determined

1. **All 100 million published number records.** The RFC Appendix B table, the pinned canonicalization vector, and the published first-1,000 checksum passed. Running all 100 million requires generating or downloading roughly 4 GB uncompressed data and adds little boundary coverage beyond the deterministic prefix and Appendix B.
2. **Exact Git compatibility floor.** Git 2.43.7 failed and 2.47.3 passed. A build-and-test bisect of upstream Git 2.44–2.47 would identify the first compatible release.
3. **Tree divergence across Git versions.** No divergence appeared across 2.47.3, 2.49.1, and Apple 2.50.1. Version-dependent operability did appear. A larger historical/future CI matrix and real repositories would be needed to prove whether a specific Git upgrade changes a clean result tree.
4. **Absolute macOS descendant containment.** No unprivileged cgroup-equivalent primitive was found. Establishing an absolute guarantee would require a separately evaluated privileged/launchd/Endpoint-Security containment design, not another process-group tweak.
5. **Kernel-forced PID reuse.** The exact safety condition was exercised with a live reused PID value and a mismatched recorded start identity, but the host kernel was not churned until it reassigned a killed holder’s PID. A Linux PID namespace with a deliberately low PID limit would make that stress test practical.
6. **Intel macOS execution.** The public `proc_bsdinfo` layout and syscall are shared, but the no-cgo start-identity code was executed only on arm64 macOS.

## Primary references

- [RFC 8785 — JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785.html)
- [RFC 8785 verified errata](https://www.rfc-editor.org/errata/rfc8785)
- [Published JCS implementation and vectors](https://github.com/cyberphone/json-canonicalization)
- [`git merge-tree` documentation](https://git-scm.com/docs/git-merge-tree)
- [`gitattributes` merge behavior](https://git-scm.com/docs/gitattributes)
- [Apple XNU `proc_info` source](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/proc_info.c)

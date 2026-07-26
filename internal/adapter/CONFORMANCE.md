# Core adapter client conformance

Each row names a counter-oracle: deleting the cited production check makes the named test fail.
Fixtures labelled `real` build and execute the checked-in command; `fake:<mode>` executes the test
binary through the same process boundary. Injection is used only where a healthy local pipe or
process table cannot deterministically produce the failure.

| Clause | Accepting fixture | Rejecting fixture | Deletion-mutation evidence |
|---|---|---|---|
| P1 current protocol is 2 | `real:claude`, `real:codex` | protocol 3 raw frame | Delete/change `ProtocolVersion` or range check → `TestRealFirstPartyAdaptersProbeAndExitCleanly` / `TestProtocolNegotiationAndFeatures/above_range` |
| P2 protocol 1 negotiation | protocol 1, features absent | protocol 0 raw frame | Delete lower-version acceptance or lower bound → `TestProtocolNegotiationAndFeatures/protocol_1_absent` / `below_range` |
| P3 protocol-1 feature absence | protocol 1, features absent | protocol 1 with `features: []` and `null` | Delete presence check → `TestProtocolNegotiationAndFeatures/protocol_1_empty_rejected` |
| P4 protocol-2 feature absence/empty | protocol 2, absent and `[]` | protocol 2, `null` | Collapse absent/null → `TestProtocolNegotiationAndFeatures/protocol_2_null_rejected` |
| P5 feature list is open and retained | unknown, duplicate, ordered tokens | non-string feature member | Drop, deduplicate, sort, or close the list → `TestProtocolNegotiationAndFeatures/protocol_2_open_ordered_list` / `protocol_2_non-string_rejected` |
| P6 required probe objects | complete raw result | adapter, capabilities, or enforcement omitted | Delete a presence check → corresponding `TestRequiredProbeShape` subtest |
| P7 required capabilities | all five booleans plus models | each capability, model id, and valid aliases shape removed/invalid in turn | Delete typed required decoding → corresponding `TestRequiredCapabilityAndModelFields` subtest |
| P8 enforcement defaults | all five booleans true | each boolean omitted in turn (accepted as false) | Delete a field assignment or default to true → corresponding `TestEnforcementPresenceDefaultsFalse` subtest |
| P9 real result interoperability | `real:claude`, `real:codex` | covered by strict raw-frame rows below; real adapters are accepting oracles | Bypass real wire decoding or alter first-party shapes → `TestRealFirstPartyAdaptersProbeAndExitCleanly` |
| D1 exact executable name | `fake:blank_success` at exact name | lookalike only | Search by prefix/suffix → `TestDiscoveryFailuresDoNotSearchLoosely/exact_name_absent` |
| D2 PATH-only discovery | exact name in snapshot PATH | PATH absent | Add default/direct lookup → `TestDiscoveryFailuresDoNotSearchLoosely/PATH_absent` |
| D3 adapter id cannot become a path | ordinary id | `../fake` | Permit slash/direct resolution → `TestDiscoveryFailuresDoNotSearchLoosely/slash_cannot_become_direct_path` |
| D4 PATH precedence | same exact name in two entries | second entry is the counter-oracle | Reverse/ignore ordered walk → `TestPATHResolutionUsesExactNameFirstEntryAndSnapshot` |
| D5 one unchanged environment snapshot | snapshot with PATH, credential, and `GIT_CONFIG_*` sent unchanged | any added, removed, overwritten, or live-re-read value | Mutate `command.Env` or use live PATH → `TestPATHResolutionUsesExactNameFirstEntryAndSnapshot` |
| D6 production snapshot boundary | `NewClient()` snapshot equals same-process `os.Environ()` element-wise | any constructor insertion, omission, replacement, or reordering | Mutate the production constructor's snapshot → `TestNewClientSnapshotsInheritedEnvironmentUnchanged` |
| D7 POSIX session leader | fake records PID equal to SID | inherited core session is the counter-oracle | Delete `Setsid` → `TestSessionLeadership` |
| W1 JSON-Lines probe request | real adapters accept the request | injected request write failure | Delete newline/full-write handling → real-adapter test / `TestSpawnAndInjectedIOFailures/request` |
| W2 blank lines skipped | `fake:blank_success` | malformed nonblank line then valid frame | Stop skipping blanks or skip malformed output → `TestBlankLinesAndPostResponseWait/blank_lines` / `TestFramingFailuresAreNotSkipped/malformed_then_valid` |
| W3 response I/O | real adapter response | injected pipe-read failure; a healthy local pipe cannot deterministically produce one | Ignore read errors → `TestSpawnAndInjectedIOFailures/response` |
| W4 complete response before EOF | real adapters | `fake:premature_eof`, `fake:partial_eof` | Accept EOF/partial frame → corresponding `TestFramingFailuresAreNotSkipped` subtest |
| W5 strict JSON shape | complete raw response | malformed or unknown field followed by valid response | Skip malformed output → `TestStrictProbeResponseFailures` / `TestFramingFailuresAreNotSkipped/malformed_then_valid` |
| W6 duplicate keys at every depth | unique raw response | incompatible duplicate followed by valid response | Delete pre-decode duplicate scan → `TestStrictProbeResponseFailures/duplicate_before_incompatible_value` and fake counterpart |
| W7 valid UTF-8 | real response | invalid UTF-8 followed by valid response | Delete UTF-8 check → `TestFramingFailuresAreNotSkipped/invalid_utf8_then_valid` |
| W8 1 MiB frame cap | real response | oversized frame followed by valid response | Delete cap/error handling → `TestFramingFailuresAreNotSkipped/oversized_then_valid` |
| W9 JSON-RPC response identity | echoed `"probe"` | mismatched id | Ignore id → `TestStrictProbeResponseFailures/mismatched_id` |
| W10 result/error exactly-one and non-null | result object | error response, null result, result plus null error | Collapse absence/null or ignore error → corresponding `TestStrictProbeResponseFailures` subtest |
| W11 one probe response then EOF | real adapters | second frame or partial frame after response | Return on first frame → `TestFramingFailuresAreNotSkipped/extra_response` / `partial_after_response` |
| W12 expected adapter identity | matching fake/real id | `fake:wrong_adapter` | Delete id comparison → `TestFramingFailuresAreNotSkipped/wrong_adapter` |
| W13 output-pipe lifetime under concurrent wait | immediate-exit fake's complete buffered response remains readable after the child is reaped | `Cmd.StdoutPipe`/`StderrPipe` let `Wait` close pending readers | Replace client-owned pipes with `Cmd` pipe helpers → `TestWaitCannotCloseOutputBeforeReadersFinish` |
| L1 spawn succeeds | real/fake executable | executable-format failure | Ignore `Start` error → `TestSpawnAndInjectedIOFailures/spawn` |
| L2 15000 ms completion ceiling | published constant plus immediate real probes | `fake:hang_no_response` | Delete response-phase timer → `TestPublishedLimits` / `TestDeadlineCoversResponseAndCleanExit/hang_no_response` |
| L3 deadline includes clean exit | `fake:delayed_exit` within deadline | `fake:hang_after_response` | Return on response or stop timer early → `TestBlankLinesAndPostResponseWait/waits_for_clean_exit` / deadline subtest |
| L4 stdin clean EOF and zero exit | `real:claude`, `real:codex` | `fake:nonzero_after_response` | Omit stdin close or `Wait`/exit check → real-adapter test / `TestFramingFailuresAreNotSkipped/nonzero_after_response` |
| L5 30000 ms outer grace | published constant | shortened fake timeout uses the same ladder | Change/remove grace → `TestPublishedLimits`; delete TERM/grace phase → `TestTimeoutSweepsEveryProcessGroupInSession` |
| L6 every process group in SID | normal one-process fake | TERM-ignoring parent plus separate-PGID child in same SID | Kill only leader/group → `TestTimeoutSweepsEveryProcessGroupInSession` |
| L7 repeated KILL and verified-empty sweep | clean session | same multi-group timeout tree | Return after first KILL or without re-enumeration → `TestTimeoutSweepsEveryProcessGroupInSession` |
| L8 stored leader and per-PID start identity | current PID with matching start identity | same PID with stale start identity | Delete either identity comparison → `TestSessionLeaderIdentityRejectsPIDReuse` / `TestIndividualSignalRequiresMatchingStartIdentity` |
| L9 unverifiable cleanup fails closed | verified-empty normal probes | injected enumeration failure; native failure is not safely reproducible | Drop cleanup diagnostic → `TestCleanupUnverifiableIsAggregated` |
| L10 stderr raw cap and sanitization | harmless stderr retained | secret plus beyond-cap sentinel | Discard, fail to redact, or buffer beyond cap → `TestStderrIsBoundedAndSanitized` |
| A1 distinct adapter exactly once | duplicate ids for `alpha`, `bad`, `zeta` | invocation marker would contain duplicates | Delete deduplication → `TestProbeAllDeduplicatesAggregatesAndOrders` |
| A2 aggregate after failure | successful `alpha` and `zeta` | `bad` fails between them | Short-circuit → `TestProbeAllDeduplicatesAggregatesAndOrders` |
| A3 deterministic report order | unsorted probes and diagnostics | exact id/kind/detail order is the counter-oracle | Delete either report sort → `TestSortReportDeterministic` |
| A4 no run-state dependency | adapter/protocol/adapterkit import set | any new production import outside the explicit list | Add run-state, network, or unrelated dependency → `TestProductionImportsAreExplicitlyAllowed` |

The nonzero-before-response case is already rejected by the independently required EOF rule; the
nonzero-after-response fixture is the deletion oracle for the exit-status check, where EOF alone
cannot mask its removal.

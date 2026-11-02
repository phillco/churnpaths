# Go port notes

## Scope and safety

The Python implementation and all 71 original tests are preserved unchanged in
`python/`. The root builds one executable with Go 1.22+ and a vendored, pinned
TOML parser. No other third-party libraries are used.

`App` injects native reads/writes, command execution, availability and ownership
persistence. `XMLPolicy` injects the owner-preserving command and race points.
Native adapter tests use fake Core Foundation handles to verify object release
on success and failures without loading frameworks. All test state/XML files
live under disposable test directories. Tests never call live native setters,
never invoke the Time Machine exclusion-adding command, and never apply through
the reference-parity harness. Native and shell entry points in CLI fixtures fail
the test unless explicitly replaced by a fake.

Python and Go parity compares bare resolver JSON semantically and ordinary text
byte-for-byte, including all-target dry-run/check/status with fake native readers, and
Backblaze fixture previews through the delivered executable. Fixtures exercise host
scope, missing paths, includes, glob links, evidence-qualified name discovery,
pruning, config/environment/flag precedence, Unicode and marked rules. Parity
builds and invokes the delivered Go executable; on non-macOS, the Backblaze
preview dispatcher is exercised with fake platform availability instead.

## Documented differences

- Help layout and parser, operating-system and TOML parser diagnostics use Go
  wording. Normal target reports, CLI flag meanings and exit codes agree.
- JSON key order and Unicode escaping may differ; decoded JSON values agree.
- Backblaze XML accepts UTF-8/ASCII only. Other declared encodings are rejected
  before any file or state mutation. The stdlib XML decoder lacks the broader
  encoding support of Python's parser.
- Native plist output must be XML, as requested by `tmutil isexcluded -X` and
  Core Foundation serialization. Binary plists are rejected. Duplicate dict
  keys and structurally invalid scalar values fail closed.
- Native Spotlight and Time Machine writes require cgo on macOS. Disabling cgo
  retains list resolution, XML handling and Time Machine reads, and returns an
  actionable error for native operations. No runtime Python or Swift is used.
- Go's Unicode tables can evolve independently of the installed Python's.
  Host matching implements full folding (including expanding folds).

## Verification boundaries

`go build`, `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting,
the original Python suite, and disabled-cgo tests are checked. Fish completions
are syntax-checked and queried without changing the user's shell configuration.
The Go suite includes over 100 top-level tests in addition to fixture subcases.
No live exclusion setting or GUI was exercised; native API availability on a
particular macOS release still requires live acceptance outside this port job.

The Python XML replacement and this port both compare the source immediately
before rename. Neither can make a separate native client's edit atomic with
that rename; an editor racing in that final interval remains an inherited limit.

## Audit exceptions

The content audit explicitly permits these bounded, non-personal patterns:

- `go.mod`: `module github.com/phillco/churnpaths` is the required public module
  identity. The same public module identity appears in verification output only.
- Generic `/Users` filesystem guards in `resolver.go`, `backblaze.go`, and their
  tests, and the unchanged equivalents in `python/churnpaths`,
  `python/backblaze_xml.py`, `python/tests/test_churnpaths.py` and
  `python/tests/test_backblaze_xml.py`. These reject whole home directories;
  they contain no user's home path.
- This exception list names those exact audit patterns solely to record them.

All other matches from the required audit must be absent. Vendor sources are
excluded as requested; the original MIT copyright notice remains intact.

## Original test mapping

All 71 Python tests have named Go equivalents below. Configuration precedence
also has executable parity coverage in `TestParityConfig`; malformed config's
CLI exit code is checked by `TestCliBadConfigReturnsTwo`. Type-invalid Python
API inputs that Go's static types cannot express are covered by invalid path,
state and plist cases. The native CF lifetime tests use injected integer
handles and assert actual release calls/order.

| Original Python test | Go equivalent |
| --- | --- |
| `test_backblaze_xml.py:XMLTests.test_add_preserves_unowned_bytes_and_backs_up` | `backblaze_test.go:TestXMLAddPreservesUnownedBytesAndBacksUp` |
| `test_backblaze_xml.py:XMLTests.test_remove_restores_original_bytes_around_block` | `backblaze_test.go:TestXMLRemoveRestoresOriginalBytesAroundBlock` |
| `test_backblaze_xml.py:XMLTests.test_prefix_ends_with_slash_so_siblings_do_not_match` | `backblaze_test.go:TestXMLPrefixEndsWithSlashSoSiblingsDoNotMatch` |
| `test_backblaze_xml.py:XMLTests.test_unsafe_prefixes_rejected` | `backblaze_test.go:TestXMLUnsafePrefixesRejected` |
| `test_backblaze_xml.py:XMLTests.test_tampered_block_is_refused` | `backblaze_test.go:TestXMLTamperedBlockIsRefused` |
| `test_backblaze_xml.py:XMLTests.test_dtd_symlink_and_missing_file_refused` | `backblaze_test.go:TestXMLDTDSymlinkAndMissingFileRefused` |
| `test_backblaze_xml.py:XMLTests.test_concurrent_change_is_not_overwritten` | `backblaze_test.go:TestXMLConcurrentChangeIsNotOverwritten` |
| `test_backblaze_xml.py:XMLTests.test_cli_dry_run_apply_and_check` | `main_test.go:TestXMLCliDryRunApplyAndCheck` |
| `test_churnpaths.py:ManifestTests.test_host_scopes_casefold_wildcard_and_spaces` | `resolver_test.go:TestManifestHostScopesCasefoldWildcardAndSpaces` |
| `test_churnpaths.py:ManifestTests.test_missing_exact_and_unmatched_glob_remain_distinct` | `resolver_test.go:TestManifestMissingExactAndUnmatchedGlobRemainDistinct` |
| `test_churnpaths.py:ManifestTests.test_glob_only_finds_immediate_children` | `resolver_test.go:TestManifestGlobOnlyFindsImmediateChildren` |
| `test_churnpaths.py:ManifestTests.test_recursive_or_parent_component_globs_rejected` | `resolver_test.go:TestManifestRecursiveOrParentComponentGlobsRejected` |
| `test_churnpaths.py:ManifestTests.test_broad_relative_and_parent_paths_rejected` | `resolver_test.go:TestManifestBroadRelativeAndParentPathsRejected` |
| `test_churnpaths.py:ManifestTests.test_glob_hit_cannot_bypass_home_folder_guard` | `resolver_test.go:TestManifestGlobHitCannotBypassHomeFolderGuard` |
| `test_churnpaths.py:ManifestTests.test_exact_file_is_not_a_folder` | `resolver_test.go:TestManifestExactFileIsNotAFolder` |
| `test_churnpaths.py:ManifestTests.test_named_virtualenv_requires_environment_evidence` | `resolver_test.go:TestManifestNamedVirtualenvRequiresEnvironmentEvidence` |
| `test_churnpaths.py:ManifestTests.test_named_walk_does_not_follow_child_symlinks_or_git` | `resolver_test.go:TestManifestNamedWalkDoesNotFollowChildSymlinksOrGit` |
| `test_churnpaths.py:ManifestTests.test_name_root_symlink_is_not_walked` | `resolver_test.go:TestManifestNameRootSymlinkIsNotWalked` |
| `test_churnpaths.py:ManifestTests.test_exact_symlink_and_symlink_ancestor_are_rejected` | `resolver_test.go:TestManifestExactSymlinkAndSymlinkAncestorAreRejected` |
| `test_churnpaths.py:ManifestTests.test_symlinked_glob_hit_is_skipped_not_fatal` | `resolver_test.go:TestManifestSymlinkedGlobHitIsSkippedNotFatal` |
| `test_churnpaths.py:ManifestTests.test_glob_matching_only_symlinks_is_not_unmatched` | `resolver_test.go:TestManifestGlobMatchingOnlySymlinksIsNotUnmatched` |
| `test_churnpaths.py:ManifestTests.test_name_discovery_stops_after_four_levels` | `resolver_test.go:TestManifestNameDiscoveryStopsAfterFourLevels` |
| `test_churnpaths.py:ManifestTests.test_matching_name_prunes_nested_matches` | `resolver_test.go:TestManifestMatchingNamePrunesNestedMatches` |
| `test_churnpaths.py:ManifestTests.test_rust_target_requires_generated_build_evidence` | `resolver_test.go:TestManifestRustTargetRequiresGeneratedBuildEvidence` |
| `test_churnpaths.py:ManifestTests.test_exact_parent_covers_nested_glob_and_named_hits` | `resolver_test.go:TestManifestExactParentCoversNestedGlobAndNamedHits` |
| `test_churnpaths.py:OwnershipTests.test_preserves_unrelated_and_preexisting_desired_exclusions` | `main_test.go:TestOwnershipPreservesUnrelatedAndPreexistingDesiredExclusions` |
| `test_churnpaths.py:OwnershipTests.test_preview_has_no_api_or_ownership_mutations` | `main_test.go:TestOwnershipPreviewHasNoAPIOrOwnershipMutations` |
| `test_churnpaths.py:OwnershipTests.test_removes_only_stale_owned_exclusion` | `main_test.go:TestOwnershipRemovesOnlyStaleOwnedExclusion` |
| `test_churnpaths.py:OwnershipTests.test_owned_folder_replaced_by_symlink_is_removed_not_fatal` | `main_test.go:TestOwnershipOwnedFolderReplacedBySymlinkIsRemovedNotFatal` |
| `test_churnpaths.py:OwnershipTests.test_missing_declared_path_retains_ownership` | `main_test.go:TestOwnershipMissingDeclaredPathRetainsOwnership` |
| `test_churnpaths.py:OwnershipTests.test_absent_stale_path_loses_ownership` | `main_test.go:TestOwnershipAbsentStalePathLosesOwnership` |
| `test_churnpaths.py:OwnershipTests.test_invalid_state_fails_before_any_api_mutation` | `main_test.go:TestOwnershipInvalidStateFailsBeforeAnyAPIMutation` |
| `test_churnpaths.py:OwnershipTests.test_wrong_host_fails_before_mutation` | `main_test.go:TestOwnershipWrongHostFailsBeforeMutation` |
| `test_churnpaths.py:OwnershipTests.test_failure_before_api_never_creates_an_exclusion` | `main_test.go:TestOwnershipFailureBeforeAPINeverCreatesAnExclusion` |
| `test_churnpaths.py:OwnershipTests.test_add_then_readback_failure_retains_ownership_for_recovery` | `main_test.go:TestOwnershipAddThenReadbackFailureRetainsOwnershipForRecovery` |
| `test_churnpaths.py:OwnershipTests.test_remove_then_failure_can_finish_ownership_cleanup` | `main_test.go:TestOwnershipRemoveThenFailureCanFinishOwnershipCleanup` |
| `test_churnpaths.py:IncludeTests.test_include_adds_exact_folders_and_skips_bad_entries` | `resolver_test.go:TestIncludeAddsExactFoldersAndSkipsBadEntries` |
| `test_churnpaths.py:ConfigTests.test_default_source_without_config` | `resolver_test.go:TestConfigDefaultSourceWithoutConfig` |
| `test_churnpaths.py:ConfigTests.test_config_file_sets_source_and_state` | `resolver_test.go:TestConfigFileSetsSourceAndState` |
| `test_churnpaths.py:ConfigTests.test_environment_beats_config_and_flag_beats_environment` | `resolver_test.go:TestConfigEnvironmentBeatsConfigAndFlagBeatsEnvironment` |
| `test_churnpaths.py:ConfigTests.test_bad_config_is_an_error` | `resolver_test.go:TestConfigBadConfigIsAnError` |
| `test_churnpaths.py:ConfigTests.test_unknown_directive_and_bad_include_line` | `resolver_test.go:TestConfigUnknownDirectiveAndBadIncludeLine` |
| `test_cli.py:CliTests.test_list_json_is_the_bare_resolver_result` | `main_test.go:TestCliListJSONIsTheBareResolverResult` |
| `test_cli.py:CliTests.test_default_skips_unavailable_backblaze_with_a_note` | `main_test.go:TestCliDefaultSkipsUnavailableBackblazeWithANote` |
| `test_cli.py:CliTests.test_explicit_unavailable_target_is_an_error` | `main_test.go:TestCliExplicitUnavailableTargetIsAnError` |
| `test_cli.py:CliTests.test_target_is_repeatable_and_all_means_everything` | `main_test.go:TestCliTargetIsRepeatableAndAllMeansEverything` |
| `test_cli.py:CliTests.test_check_exits_one_on_drift_in_any_target` | `main_test.go:TestCliCheckExitsOneOnDriftInAnyTarget` |
| `test_cli.py:CliTests.test_apply_for_another_host_is_refused` | `main_test.go:TestCliApplyForAnotherHostIsRefused` |
| `test_cli.py:CliTests.test_dry_run_conflicts_with_apply_and_list_with_modes` | `main_test.go:TestCliDryRunConflictsWithApplyAndListWithModes` |
| `test_cli.py:CliTests.test_one_failing_target_does_not_stop_the_others` | `main_test.go:TestCliOneFailingTargetDoesNotStopTheOthers` |
| `test_cli.py:CliTests.test_backblaze_preview_says_dry_run_and_writes_nothing` | `main_test.go:TestCliBackblazePreviewSaysDryRunAndWritesNothing` |
| `test_cli.py:CliTests.test_status_shows_backblaze_state_as_json` | `main_test.go:TestCliStatusShowsBackblazeStateAsJSON` |
| `test_spotlight.py:PublicAdapterTests.test_reads_without_mutation` | `native_test.go:TestPublicAdapterReadsWithoutMutation` |
| `test_spotlight.py:PublicAdapterTests.test_add_and_remove_confirm_readback` | `native_test.go:TestPublicAdapterAddAndRemoveConfirmReadback` |
| `test_spotlight.py:PublicAdapterTests.test_readback_mismatch_is_an_error` | `native_test.go:TestPublicAdapterReadbackMismatchIsAnError` |
| `test_spotlight.py:PublicAdapterTests.test_api_failure_does_not_claim_success` | `native_test.go:TestPublicAdapterAPIFailureDoesNotClaimSuccess` |
| `test_spotlight.py:PublicAdapterTests.test_bad_paths_never_load_framework` | `native_test.go:TestPublicAdapterBadPathsNeverLoadFramework` |
| `test_spotlight.py:PublicAdapterTests.test_unsupported_platform_is_actionable` | `native_test.go:TestPublicAdapterUnsupportedPlatformIsActionable` |
| `test_spotlight.py:PublicAdapterTests.test_missing_private_symbol_is_actionable` | `native_test.go:TestPublicAdapterMissingPrivateSymbolIsActionable` |
| `test_spotlight.py:CoreFoundationLifetimeTests.test_unicode_paths_and_owned_objects_release` | `native_test.go:TestCoreFoundationUnicodePathsAndOwnedObjectsRelease` |
| `test_spotlight.py:CoreFoundationLifetimeTests.test_invalid_types_still_release_owned_objects` | `native_test.go:TestCoreFoundationInvalidTypesStillReleaseOwnedObjects` |
| `test_spotlight.py:CoreFoundationLifetimeTests.test_malformed_xml_still_releases_owned_objects` | `native_test.go:TestCoreFoundationMalformedXMLStillReleasesOwnedObjects` |
| `test_spotlight.py:CoreFoundationLifetimeTests.test_failed_serialization_releases_array_only` | `native_test.go:TestCoreFoundationFailedSerializationReleasesArrayOnly` |
| `test_spotlight.py:CoreFoundationLifetimeTests.test_rejected_write_releases_string` | `native_test.go:TestCoreFoundationRejectedWriteReleasesString` |
| `test_timemachine.py:TimeMachineTests.test_resolves_present_and_missing` | `main_test.go:TestTimeMachineResolvesPresentAndMissing` |
| `test_timemachine.py:TimeMachineTests.test_dry_run_writes_nothing` | `main_test.go:TestTimeMachineDryRunWritesNothing` |
| `test_timemachine.py:TimeMachineTests.test_apply_only_writes_included_folders_and_reads_back` | `main_test.go:TestTimeMachineApplyOnlyWritesIncludedFoldersAndReadsBack` |
| `test_timemachine.py:TimeMachineTests.test_check_reports_included` | `main_test.go:TestTimeMachineCheckReportsIncluded` |
| `test_timemachine.py:TimeMachineTests.test_partial_tmutil_output_is_an_error` | `native_test.go:TestTimeMachinePartialTmutilOutputIsAnError` |
| `test_timemachine.py:TimeMachineTests.test_check_and_apply_conflict` | `main_test.go:TestTimeMachineCheckAndApplyConflict` |
| `test_timemachine.py:TimeMachineTests.test_default_preview_writes_nothing_and_status_reads` | `main_test.go:TestTimeMachineDefaultPreviewWritesNothingAndStatusReads` |

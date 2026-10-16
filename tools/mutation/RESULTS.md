# Mutation check results

Run on 2026-10-16 at commit `95e8b73` with `python3 tools/mutation/run.py all`.
**96 of 96 mutations caught.** Each row breaks one protection; a test must fail.

| Kind | Mutation | Result | First failing test |
|---|---|---|---|
| backend | regex: input length cap removed | CAUGHT | --- FAIL: TestRegexOperator (0.05s) |
| backend | regex: pattern length cap removed | CAUGHT | --- FAIL: TestValidateRegex (0.00s) |
| backend | regex: save-time compile check removed | CAUGHT | --- FAIL: TestValidateRegex (0.00s) |
| backend | regex: rule validation does not call ValidateRegex | CAUGHT | --- FAIL: TestValidateNewOperators (0.00s) |
| backend | negation: indeterminate operands are negated too | CAUGHT | --- FAIL: TestStringOperators (0.00s) |
| backend | negation: missing attribute is negated | CAUGHT | --- FAIL: TestStringOperators (0.00s) |
| backend | semver: invalid versions compare as zero | CAUGHT | --- FAIL: TestSemverOperators (0.00s) |
| backend | dates: any layout accepted (date-only) | CAUGHT | --- FAIL: TestDateOperators (0.00s) |
| backend | segments: a missing segment counts as 'not in' (widens rules) | CAUGHT | --- FAIL: TestDeletedSegmentReferencedByStaleConfigMatchesNobody (0.00s) |
| backend | segments: segment rules may nest | CAUGHT | --- FAIL: TestValidateNewOperators (0.00s) |
| backend | segments: explicit members ignored | CAUGHT | --- FAIL: TestSegmentMembershipIsMemberOrRules (0.00s) |
| backend | segments: rules ORed instead of ANDed | CAUGHT | --- FAIL: TestSegmentMembershipIsMemberOrRules (0.00s) |
| backend | flags: unknown segment keys accepted in flag rules | CAUGHT | --- FAIL: TestFlagRulesCannotReferenceUnknownSegmentsOrUseBadOperators (0.15s) |
| backend | quota: member limit not enforced | CAUGHT | --- FAIL: TestSegmentValidation (0.14s) |
| backend | quota: rule size not enforced | CAUGHT | --- FAIL: TestSegmentValidation (0.18s) |
| backend | quota: segment count not enforced | CAUGHT | --- FAIL: TestSegmentCountQuota (0.13s) |
| backend | segments: no workspace lock on create (count race) | CAUGHT | --- FAIL: TestSegmentCountQuotaHoldsUnderConcurrency (0.13s) |
| backend | segments: audit dumps every member | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.57s) |
| backend | segments: no event published | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (2.16s) |
| backend | segments: event goes to the wrong workspace channel | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (2.16s) |
| backend | segments: flag config versions not bumped | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.57s) |
| backend | segments: update not audited | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.59s) |
| backend | segments: delete of an in-use segment needs no confirmation | CAUGHT | --- FAIL: TestDeleteSegment (0.14s) |
| backend | segments: evaluation query not scoped to the workspace | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.19s) |
| backend | segments: read not scoped to the workspace (Get) | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.11s) |
| backend | segments: viewers may write | CAUGHT | --- FAIL: TestSegmentRoles (0.11s) |
| backend | segments: member lookup is not indexed (scan the members) | CAUGHT | --- FAIL: TestMemberLookupUsesTheIndex (0.21s) |
| backend | schema: composite foreign key replaced by a plain one | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.28s) |
| backend | legacy: string equals becomes case-insensitive (golden) | CAUGHT | --- FAIL: TestLegacyEvaluationIsByteForByteUnchanged (0.01s) |
| backend | legacy: contains on non-string now coerces (golden) | CAUGHT | --- FAIL: TestNegationOfEveryLegacyOperator (0.00s) |
| backend | rate limit: removed from segment create | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.11s) |
| backend | rate limit: removed from segment update | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.10s) |
| backend | rate limit: removed from segment delete | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.10s) |
| backend | rollback check: exists counted as a new feature | CAUGHT | --- FAIL: TestUsesNewFeaturesSQL (0.16s) |
| backend | rollback check: negate not counted | CAUGHT | --- FAIL: TestUsesNewFeaturesSQL (0.16s) |
| backend | audit: workspace-level rows left out of the environment log | CAUGHT | --- FAIL: TestWorkspaceLevelAuditRowsAreListedOnce (0.14s) |
| backend | scheduling: the creator's role is not re-checked at execution | CAUGHT | --- FAIL: TestTheCreatorsRoleIsRecheckedAtExecution (0.13s) |
| backend | scheduling: an approval-gated change can be scheduled by an editor | CAUGHT | --- FAIL: TestSchedulingNeedsTheRoleOfTheChange (0.14s) |
| backend | scheduling: a kill needs the approval role | CAUGHT | --- FAIL: TestSchedulingNeedsTheRoleOfTheChange (0.14s) |
| backend | scheduling: a worker without the lease still executes | CAUGHT | --- FAIL: TestAStaleWorkerCannotExecuteARowThatWasTakenOver (0.10s) |
| backend | scheduling: the executor ignores a cancel (status not re-read) | CAUGHT | --- FAIL: TestCancelRacingWithExecutionNeverHalfApplies (0.11s) |
| backend | scheduling: a cancel cannot reach a claimed (running) change | CAUGHT | --- FAIL: TestCancelRacingWithExecutionNeverHalfApplies (0.12s) |
| backend | scheduling: expired leases are never taken over | CAUGHT | --- FAIL: TestACrashBetweenClaimAndCompletionIsRecovered (0.12s) |
| backend | scheduling: a stale change is not marked missed | CAUGHT | --- FAIL: TestOverdueBeyondGraceIsMissedNotApplied (0.12s) |
| backend | scheduling: retries have no bound | CAUGHT | --- FAIL: TestRetriesStopAtTheBound (0.13s) |
| backend | scheduling: retries have no backoff | CAUGHT | --- FAIL: TestRetriesStopAtTheBound (0.12s) |
| backend | scheduling: an idempotency key can be reused for another change | CAUGHT | --- FAIL: TestIdempotencyKeys (0.12s) |
| backend | scheduling: a patch that conflicts is applied anyway | CAUGHT | --- FAIL: TestAPatchThatNoLongerAppliesCleanlyFailsWithAConflict (0.12s) |
| backend | scheduling: a patch is not validated when scheduled | CAUGHT | --- FAIL: TestSchedulingValidatesLikeAnEdit (0.12s) |
| backend | scheduling: no cap on pending changes per flag | CAUGHT | --- FAIL: TestIdempotencyKeys (0.13s) |
| backend | scheduling: no workspace lock on the cap (a race) | CAUGHT | --- FAIL: TestSchedulingCapHoldsUnderConcurrency (0.14s) |
| backend | scheduling: no minimum lead time | CAUGHT | --- FAIL: TestSchedulingValidatesLikeAnEdit (0.12s) |
| backend | scheduling: the scheduler's edits are not marked via schedule | CAUGHT | --- FAIL: TestScheduledEnableRunsOnThroughTheEditPath (0.15s) |
| backend | scheduling: cancel is not audited | CAUGHT | --- FAIL: TestEveryStateChangeIsAudited (0.14s) |
| backend | scheduling: the scheduled change is not scoped to the workspace on read | CAUGHT | --- FAIL: TestScheduledChangesAreTenantScoped (0.12s) |
| backend | internal endpoint: the secret is compared with == | CAUGHT | --- FAIL: TestInternalHandlerComparesInConstantTime (0.00s) |
| backend | internal endpoint: a browser (Origin header) is accepted | CAUGHT | --- FAIL: TestInternalEndpointNeedsTheSecret (0.09s) |
| backend | internal endpoint: enabled with no secret | CAUGHT | --- FAIL: TestInternalEndpointIsDisabledWhenUnset (0.52s) |
| backend | internal endpoint: not rate limited | CAUGHT | --- FAIL: TestInternalEndpointIsRateLimitedBeforeTheSecretIsChecked (0.09s) |
| backend | internal endpoint: CORS headers on /internal | CAUGHT | --- FAIL: TestInternalPathsGetNoCORS (0.00s) |
| backend | promotion: the expected version is not enforced | CAUGHT | --- FAIL: TestPromotionNeverOverwritesAConcurrentEdit (0.12s) |
| backend | promotion: the enabled state is copied by default | CAUGHT | --- FAIL: TestPromotionPreviewAndApply (0.15s) |
| backend | promotion: apply needs only an editor in production | CAUGHT | --- FAIL: TestPromotionUsesTheEditGate (0.12s) |
| backend | promotion: the source is not scoped to the workspace | CAUGHT | --- FAIL: TestPromotionIsTenantScoped (0.13s) |
| backend | promotion: the source version is not enforced | CAUGHT | --- FAIL: TestPromotionNeverOverwritesAConcurrentEdit (0.12s) |
| backend | promotion: the source is not named in the audit entry | CAUGHT | --- FAIL: TestPromotionPreviewAndApply (0.15s) |
| frontend | members: repeats not folded | CAUGHT | Error: expect(received).toEqual(expected) // deep equality |
| frontend | members: limit not enforced on merge | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | members: paste size cap removed | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | members: invalid entries added anyway | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | members: over-long key allowed | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | members: control characters allowed | CAUGHT | Error: expect(received).toEqual(expected) // deep equality |
| frontend | members: a refused paste is partly applied | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | targeting: exists can be negated | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | targeting: payload drops negate | CAUGHT | Error: expect(received).toEqual(expected) // deep equality |
| frontend | targeting: semver not checked | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | targeting: RFC 3339 not checked | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | targeting: regex length not checked | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | targeting: unknown segment accepted | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | targeting: unknown clause fields not locked | CAUGHT | Error: expect(received).not.toBeNull() |
| frontend | targeting: changing operator keeps a negated exists | CAUGHT | Error: expect(received).toMatchObject(expected) |
| frontend | schedule: a time that does not exist is not detected | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | schedule: the repeated hour is read as the later one | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | schedule: the minimum lead is not checked | CAUGHT | TypeError: expect(received).toMatch(expected) |
| frontend | schedule: a kill needs the approval role | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | schedule: UTC is shown in the browser's zone | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | schedule: daylight saving is ignored (offsets taken from today) | CAUGHT | Error: 2026-01-15T12:00 in America/New_York is gap |
| ui | ui: segment rules offer the segment operators (nesting) | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | ui: deleting an in-use segment needs no tick | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | ui: member editor draws every member | CAUGHT | Error: expect(received).toBeLessThanOrEqual(expected) |
| ui | ui: workspace-level audit rows not labelled | CAUGHT | Error: expect(locator).toHaveText(expected) failed |
| ui | ui: Escape in a dialog also closes the drawer | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | ui: viewers are offered 'Schedule a change' | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | ui: promotion copies the enabled state by default | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | ui: the exact UTC time is not stated | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | ui: an editor may pick a production enable | CAUGHT | Error: expect(locator).toContainText(expected) failed |

# Mutation check results

Run on 2026-10-16 at commit `0d893c5` with `python3 tools/mutation/run.py all`.
**136 of 136 mutations caught.** Each row breaks one protection; a test must fail.

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
| backend | quota: segment count not enforced | CAUGHT | --- FAIL: TestSegmentCountQuota (0.14s) |
| backend | segments: no workspace lock on create (count race) | CAUGHT | --- FAIL: TestSegmentCountQuotaHoldsUnderConcurrency (0.14s) |
| backend | segments: audit dumps every member | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.58s) |
| backend | segments: no event published | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (2.17s) |
| backend | segments: event goes to the wrong workspace channel | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (2.16s) |
| backend | segments: flag config versions not bumped | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.58s) |
| backend | segments: update not audited | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.57s) |
| backend | segments: delete of an in-use segment needs no confirmation | CAUGHT | --- FAIL: TestDeleteSegment (0.15s) |
| backend | segments: evaluation query not scoped to the workspace | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.19s) |
| backend | segments: read not scoped to the workspace (Get) | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.11s) |
| backend | segments: viewers may write | CAUGHT | --- FAIL: TestSegmentRoles (0.11s) |
| backend | segments: member lookup is not indexed (scan the members) | CAUGHT | --- FAIL: TestMemberLookupUsesTheIndex (0.22s) |
| backend | schema: composite foreign key replaced by a plain one | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.19s) |
| backend | legacy: string equals becomes case-insensitive (golden) | CAUGHT | --- FAIL: TestLegacyEvaluationIsByteForByteUnchanged (0.01s) |
| backend | legacy: contains on non-string now coerces (golden) | CAUGHT | --- FAIL: TestNegationOfEveryLegacyOperator (0.00s) |
| backend | rate limit: removed from segment create | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.11s) |
| backend | rate limit: removed from segment update | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.10s) |
| backend | rate limit: removed from segment delete | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.10s) |
| backend | rollback check: exists counted as a new feature | CAUGHT | --- FAIL: TestUsesNewFeaturesSQL (0.16s) |
| backend | rollback check: negate not counted | CAUGHT | --- FAIL: TestUsesNewFeaturesSQL (0.17s) |
| backend | audit: workspace-level rows left out of the environment log | CAUGHT | --- FAIL: TestWorkspaceLevelAuditRowsAreListedOnce (0.13s) |
| backend | scheduling: the creator's role is not re-checked at execution | CAUGHT | --- FAIL: TestTheCreatorsRoleIsRecheckedAtExecution (0.14s) |
| backend | scheduling: an approval-gated change can be scheduled by an editor | CAUGHT | --- FAIL: TestSchedulingNeedsTheRoleOfTheChange (0.14s) |
| backend | scheduling: a kill needs the approval role | CAUGHT | --- FAIL: TestSchedulingNeedsTheRoleOfTheChange (0.14s) |
| backend | scheduling: a worker without the lease still executes | CAUGHT | --- FAIL: TestAStaleWorkerCannotExecuteARowThatWasTakenOver (0.11s) |
| backend | scheduling: the executor ignores a cancel (status not re-read) | CAUGHT | --- FAIL: TestCancelRacingWithExecutionNeverHalfApplies (0.12s) |
| backend | scheduling: a cancel cannot reach a claimed (running) change | CAUGHT | --- FAIL: TestCancelRacingWithExecutionNeverHalfApplies (0.12s) |
| backend | scheduling: expired leases are never taken over | CAUGHT | --- FAIL: TestACrashBetweenClaimAndCompletionIsRecovered (0.13s) |
| backend | scheduling: a stale change is not marked missed | CAUGHT | --- FAIL: TestOverdueBeyondGraceIsMissedNotApplied (0.12s) |
| backend | scheduling: retries have no bound | CAUGHT | --- FAIL: TestRetriesStopAtTheBound (0.13s) |
| backend | scheduling: retries have no backoff | CAUGHT | --- FAIL: TestRetriesStopAtTheBound (0.13s) |
| backend | scheduling: an idempotency key can be reused for another change | CAUGHT | --- FAIL: TestIdempotencyKeys (0.12s) |
| backend | scheduling: a patch that conflicts is applied anyway | CAUGHT | --- FAIL: TestAPatchThatNoLongerAppliesCleanlyFailsWithAConflict (0.13s) |
| backend | scheduling: a patch is not validated when scheduled | CAUGHT | --- FAIL: TestSchedulingValidatesLikeAnEdit (0.13s) |
| backend | scheduling: no cap on pending changes per flag | CAUGHT | --- FAIL: TestIdempotencyKeys (0.14s) |
| backend | scheduling: no workspace lock on the cap (a race) | CAUGHT | --- FAIL: TestSchedulingCapHoldsUnderConcurrency (0.13s) |
| backend | scheduling: no minimum lead time | CAUGHT | --- FAIL: TestSchedulingValidatesLikeAnEdit (0.13s) |
| backend | scheduling: the scheduler's edits are not marked via schedule | CAUGHT | --- FAIL: TestScheduledEnableRunsOnThroughTheEditPath (0.17s) |
| backend | scheduling: cancel is not audited | CAUGHT | --- FAIL: TestEveryStateChangeIsAudited (0.14s) |
| backend | scheduling: the scheduled change is not scoped to the workspace on read | CAUGHT | --- FAIL: TestScheduledChangesAreTenantScoped (0.13s) |
| backend | internal endpoint: the secret is compared with == | CAUGHT | --- FAIL: TestInternalHandlerComparesInConstantTime (0.00s) |
| backend | internal endpoint: a browser (Origin header) is accepted | CAUGHT | --- FAIL: TestInternalEndpointNeedsTheSecret (0.10s) |
| backend | internal endpoint: enabled with no secret | CAUGHT | --- FAIL: TestInternalEndpointIsDisabledWhenUnset (0.42s) |
| backend | internal endpoint: not rate limited | CAUGHT | --- FAIL: TestInternalEndpointIsRateLimitedBeforeTheSecretIsChecked (0.09s) |
| backend | internal endpoint: CORS headers on /internal | CAUGHT | --- FAIL: TestInternalPathsGetNoCORS (0.00s) |
| backend | promotion: the expected version is not enforced | CAUGHT | --- FAIL: TestPromotionNeverOverwritesAConcurrentEdit (0.13s) |
| backend | promotion: the enabled state is copied by default | CAUGHT | --- FAIL: TestPromotionPreviewAndApply (0.17s) |
| backend | promotion: apply needs only an editor in production | CAUGHT | --- FAIL: TestPromotionUsesTheEditGate (0.13s) |
| backend | promotion: the source is not scoped to the workspace | CAUGHT | --- FAIL: TestPromotionIsTenantScoped (0.13s) |
| backend | promotion: the source version is not enforced | CAUGHT | --- FAIL: TestPromotionNeverOverwritesAConcurrentEdit (0.15s) |
| backend | promotion: the source is not named in the audit entry | CAUGHT | --- FAIL: TestPromotionPreviewAndApply (0.16s) |
| backend | tokens: every route is token-allowed | CAUGHT | --- FAIL: TestEveryRouteIsClassifiedForTokens (0.12s) |
| backend | tokens: an unclassified route is not reported | CAUGHT | --- FAIL: TestEveryRouteIsClassifiedForTokens (0.14s) |
| backend | tokens: token management is token-allowed | CAUGHT | --- FAIL: TestTokensCannotManageTokensAndAdminsCan (0.13s) |
| backend | tokens: role not capped by the creator (environment routes) | CAUGHT | --- FAIL: TestEffectiveRoleIsTheLowerOfTheTokenAndItsCreator (0.13s) |
| backend | tokens: role not capped by the creator (workspace routes) | CAUGHT | --- FAIL: TestEffectiveRoleIsTheLowerOfTheTokenAndItsCreator (0.13s) |
| backend | tokens: the higher of the two roles wins | CAUGHT | --- FAIL: TestTokenAllowlistAcrossEveryRoute (0.17s) |
| backend | tokens: environment restriction not enforced | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.14s) |
| backend | tokens: workspace of the token not checked (environment routes) | CAUGHT | --- FAIL: TestATokenOfOneWorkspaceReachesNothingOfAnother (0.14s) |
| backend | tokens: workspace of the token not checked (workspace routes) | CAUGHT | --- FAIL: TestATokenOfOneWorkspaceReachesNothingOfAnother (0.15s) |
| backend | tokens: restricted tokens may change workspace-level things | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.15s) |
| backend | tokens: expiry not checked for a cached token | CAUGHT | --- FAIL: TestExpiryAndTheCacheBound (0.14s) |
| backend | tokens: expiry not checked at lookup | CAUGHT | --- FAIL: TestMalformedTokensAreAllTheSameGenericRefusal (0.13s) |
| backend | tokens: a revoked token is accepted | CAUGHT | --- FAIL: TestTokensCannotManageTokensAndAdminsCan (0.14s) |
| backend | tokens: the cache TTL is ignored | CAUGHT | --- FAIL: TestExpiryAndTheCacheBound (0.13s) |
| backend | tokens: a revocation is not announced | CAUGHT | --- FAIL: TestRevocationReachesOtherInstances (6.29s) |
| backend | tokens: a revocation is not dropped locally | CAUGHT | --- FAIL: TestRevocationIsImmediateOnTheInstanceThatHandledItEvenWithoutABroadcast (0.12s) |
| backend | tokens: last_used written on every request | CAUGHT | --- FAIL: TestLastUsedIsWrittenAtMostOncePerMinute (0.14s) |
| backend | tokens: failed attempts are checked after the work, not before | CAUGHT | --- FAIL: TestFailedTokenAttemptsAreLimitedPerIPBeforeAnyHashingOrDatabaseWork (0.12s) |
| backend | tokens: failures are not counted | CAUGHT | --- FAIL: TestFailedTokenAttemptsAreLimitedPerIPBeforeAnyHashingOrDatabaseWork (0.12s) |
| backend | tokens: no per-token rate limit | CAUGHT | --- FAIL: TestEachTokenHasItsOwnRateLimitAndTheCreatorKeepsTheirs (0.11s) |
| backend | tokens: a token's traffic counts against its creator | CAUGHT | --- FAIL: TestATokensTrafficDoesNotThrottleItsCreator (0.13s) |
| backend | tokens: the audit actor is not taken from the token | CAUGHT | --- FAIL: TestAuditEntriesShowTheTokenAsTheActor (0.14s) |
| backend | tokens: no cap on tokens per person | CAUGHT | --- FAIL: TestMaximumActiveTokensQuota (0.13s) |
| backend | tokens: no workspace lock on the token cap (a race) | CAUGHT | --- FAIL: TestTokenCapHoldsUnderConcurrency (0.23s) |
| backend | tokens: a token may live longer than a year | CAUGHT | --- FAIL: TestTokensCannotManageTokensAndAdminsCan (0.15s) |
| backend | tokens: the credential is also read from the query string | CAUGHT | --- FAIL: TestATokenIsOnlyAcceptedInTheAuthorizationBearerHeader (0.13s) |
| backend | tokens: an expired token gets a different answer | CAUGHT | --- FAIL: TestExpiryAndTheCacheBound (0.12s) |
| backend | tokens: the hash is compared with == | CAUGHT | --- FAIL: TestTokenCodeUsesConstantTimeComparisonAndOnlyTheHeader (0.00s) |
| backend | tokens: a bad token is written to the log | CAUGHT | --- FAIL: TestTokenCodeUsesConstantTimeComparisonAndOnlyTheHeader (0.00s) |
| backend | tokens: a restricted token can promote from another environment | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.14s) |
| backend | tokens: segment usage reveals other environments to a restricted token | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.14s) |
| backend | tokens: environments listing unfiltered | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.14s) |
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
| frontend | tokens ui: a token can be given the owner role | CAUGHT | Error: expect(received).toEqual(expected) // deep equality |
| frontend | tokens ui: a 'never expires' choice | CAUGHT | Error: expect(received).toBeGreaterThan(expected) |
| frontend | tokens ui: a never-used token is not said to be never used | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | tokens ui: expiry shown in the browser's zone | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| ui | ui: segment rules offer the segment operators (nesting) | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | ui: deleting an in-use segment needs no tick | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | ui: member editor draws every member | CAUGHT | Error: expect(received).toBeLessThanOrEqual(expected) |
| ui | ui: workspace-level audit rows not labelled | CAUGHT | Error: expect(locator).toHaveText(expected) failed |
| ui | ui: Escape in a dialog also closes the drawer | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | ui: viewers are offered 'Schedule a change' | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | ui: promotion copies the enabled state by default | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | ui: the exact UTC time is not stated | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | ui: an editor may pick a production enable | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | tokens ui: the shown-once dialog can be dismissed by accident | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | tokens ui: Done works before the token is stored | CAUGHT | Error: expect(locator).toBeDisabled() failed |
| ui | tokens ui: members below admin are offered Create token | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | tokens ui: the audit log does not show the token as the actor | CAUGHT | Error: expect(locator).toBeVisible() failed |

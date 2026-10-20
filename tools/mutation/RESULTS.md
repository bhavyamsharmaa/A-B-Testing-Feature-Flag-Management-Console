# Mutation check results

Run on 2026-10-20 at commit `d494b6f` with `python3 tools/mutation/run.py all`.
**399 of 399 mutations caught.** Each row breaks one protection; a test must fail.
The run took 37 min 33 s with the backend entries spread over 4 worktrees.

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
| backend | flags: unknown segment keys accepted in flag rules | CAUGHT | --- FAIL: TestFlagRulesCannotReferenceUnknownSegmentsOrUseBadOperators (0.14s) |
| backend | quota: member limit not enforced | CAUGHT | --- FAIL: TestSegmentValidation (0.15s) |
| backend | quota: rule size not enforced | CAUGHT | --- FAIL: TestSegmentValidation (0.35s) |
| backend | quota: segment count not enforced | CAUGHT | --- FAIL: TestSegmentCountQuota (0.21s) |
| backend | segments: no workspace lock on create (count race) | CAUGHT | --- FAIL: TestSegmentCountQuotaHoldsUnderConcurrency (0.13s) |
| backend | segments: audit dumps every member | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.60s) |
| backend | segments: no event published | CAUGHT | --- FAIL: TestRealRedisRevocationAndChannelNames (1.75s) |
| backend | segments: event goes to the wrong workspace channel | CAUGHT | --- FAIL: TestRealRedisRevocationAndChannelNames (1.85s) |
| backend | segments: flag config versions not bumped | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.64s) |
| backend | segments: update not audited | CAUGHT | --- FAIL: TestSegmentChangesPropagateAndAreAudited (0.60s) |
| backend | segments: delete of an in-use segment needs no confirmation | CAUGHT | --- FAIL: TestDeleteSegment (0.15s) |
| backend | segments: evaluation query not scoped to the workspace | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.19s) |
| backend | segments: read not scoped to the workspace (Get) | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.16s) |
| backend | segments: viewers may write | CAUGHT | --- FAIL: TestSegmentRoles (0.12s) |
| backend | segments: member lookup is not indexed (scan the members) | CAUGHT | --- FAIL: TestMemberLookupUsesTheIndex (0.24s) |
| backend | schema: composite foreign key replaced by a plain one | CAUGHT | --- FAIL: TestSegmentsAreTenantScoped (0.19s) |
| backend | legacy: string equals becomes case-insensitive (golden) | CAUGHT | --- FAIL: TestLegacyEvaluationIsByteForByteUnchanged (0.00s) |
| backend | legacy: contains on non-string now coerces (golden) | CAUGHT | --- FAIL: TestNegationOfEveryLegacyOperator (0.00s) |
| backend | rate limit: removed from segment create | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.10s) |
| backend | rate limit: removed from segment update | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.13s) |
| backend | rate limit: removed from segment delete | CAUGHT | --- FAIL: TestSegmentWritesAreRateLimited (0.12s) |
| backend | rollback check: exists counted as a new feature | CAUGHT | --- FAIL: TestUsesNewFeaturesSQL (0.16s) |
| backend | rollback check: negate not counted | CAUGHT | --- FAIL: TestUsesNewFeaturesSQL (0.17s) |
| backend | audit: workspace-level rows left out of the environment log | CAUGHT | --- FAIL: TestWorkspaceLevelAuditRowsAreListedOnce (0.13s) |
| backend | scheduling: the creator's role is not re-checked at execution | CAUGHT | --- FAIL: TestTheCreatorsRoleIsRecheckedAtExecution (0.13s) |
| backend | scheduling: an approval-gated change can be scheduled by an editor | CAUGHT | --- FAIL: TestSchedulingNeedsTheRoleOfTheChange (0.14s) |
| backend | scheduling: a kill needs the approval role | CAUGHT | --- FAIL: TestSchedulingNeedsTheRoleOfTheChange (0.14s) |
| backend | scheduling: a worker without the lease still executes | CAUGHT | --- FAIL: TestAStaleWorkerCannotExecuteARowThatWasTakenOver (0.13s) |
| backend | scheduling: the executor ignores a cancel (status not re-read) | CAUGHT | --- FAIL: TestCancelRacingWithExecutionNeverHalfApplies (0.16s) |
| backend | scheduling: a cancel cannot reach a claimed (running) change | CAUGHT | --- FAIL: TestCancelRacingWithExecutionNeverHalfApplies (0.17s) |
| backend | scheduling: expired leases are never taken over | CAUGHT | --- FAIL: TestACrashBetweenClaimAndCompletionIsRecovered (0.13s) |
| backend | scheduling: a stale change is not marked missed | CAUGHT | --- FAIL: TestOverdueBeyondGraceIsMissedNotApplied (0.14s) |
| backend | scheduling: retries have no bound | CAUGHT | --- FAIL: TestRetriesStopAtTheBound (0.14s) |
| backend | scheduling: retries have no backoff | CAUGHT | --- FAIL: TestRetriesStopAtTheBound (0.14s) |
| backend | scheduling: an idempotency key can be reused for another change | CAUGHT | --- FAIL: TestIdempotencyKeys (0.12s) |
| backend | scheduling: a patch that conflicts is applied anyway | CAUGHT | --- FAIL: TestAPatchThatNoLongerAppliesCleanlyFailsWithAConflict (0.13s) |
| backend | scheduling: a patch is not validated when scheduled | CAUGHT | --- FAIL: TestSchedulingValidatesLikeAnEdit (0.13s) |
| backend | scheduling: no cap on pending changes per flag | CAUGHT | --- FAIL: TestIdempotencyKeys (0.15s) |
| backend | scheduling: no workspace lock on the cap (a race) | CAUGHT | --- FAIL: TestSchedulingCapHoldsUnderConcurrency (0.24s) |
| backend | scheduling: no minimum lead time | CAUGHT | --- FAIL: TestSchedulingValidatesLikeAnEdit (0.13s) |
| backend | scheduling: the scheduler's edits are not marked via schedule | CAUGHT | --- FAIL: TestScheduledEnableRunsOnThroughTheEditPath (0.17s) |
| backend | scheduling: cancel is not audited | CAUGHT | --- FAIL: TestEveryStateChangeIsAudited (0.15s) |
| backend | scheduling: the scheduled change is not scoped to the workspace on read | CAUGHT | --- FAIL: TestScheduledChangesAreTenantScoped (0.22s) |
| backend | internal endpoint: the secret is compared with == | CAUGHT | --- FAIL: TestInternalHandlerComparesInConstantTime (0.00s) |
| backend | internal endpoint: a browser (Origin header) is accepted | CAUGHT | --- FAIL: TestInternalEndpointNeedsTheSecret (0.10s) |
| backend | internal endpoint: enabled with no secret | CAUGHT | --- FAIL: TestInternalEndpointIsDisabledWhenUnset (0.46s) |
| backend | internal endpoint: not rate limited | CAUGHT | --- FAIL: TestInternalEndpointIsRateLimitedBeforeTheSecretIsChecked (0.10s) |
| backend | internal endpoint: CORS headers on /internal | CAUGHT | --- FAIL: TestInternalPathsGetNoCORS (0.00s) |
| backend | promotion: the expected version is not enforced | CAUGHT | --- FAIL: TestPromotionNeverOverwritesAConcurrentEdit (0.23s) |
| backend | promotion: the enabled state is copied by default | CAUGHT | --- FAIL: TestPromotionPreviewAndApply (0.18s) |
| backend | promotion: apply needs only an editor in production | CAUGHT | --- FAIL: TestPromotionUsesTheEditGate (0.15s) |
| backend | promotion: the source is not scoped to the workspace | CAUGHT | --- FAIL: TestPromotionIsTenantScoped (0.13s) |
| backend | promotion: the source version is not enforced | CAUGHT | --- FAIL: TestPromotionNeverOverwritesAConcurrentEdit (0.13s) |
| backend | promotion: the source is not named in the audit entry | CAUGHT | --- FAIL: TestPromotionPreviewAndApply (0.16s) |
| backend | tokens: every route is token-allowed | CAUGHT | --- FAIL: TestEveryRouteIsClassifiedForTokens (0.13s) |
| backend | tokens: an unclassified route is not reported | CAUGHT | --- FAIL: TestEveryRouteIsClassifiedForTokens (0.14s) |
| backend | tokens: token management is token-allowed | CAUGHT | --- FAIL: TestTokensCannotManageTokensAndAdminsCan (0.14s) |
| backend | tokens: role not capped by the creator (environment routes) | CAUGHT | --- FAIL: TestEffectiveRoleIsTheLowerOfTheTokenAndItsCreator (0.13s) |
| backend | tokens: role not capped by the creator (workspace routes) | CAUGHT | --- FAIL: TestEffectiveRoleIsTheLowerOfTheTokenAndItsCreator (0.14s) |
| backend | tokens: the higher of the two roles wins | CAUGHT | --- FAIL: TestTokenAllowlistAcrossEveryRoute (0.16s) |
| backend | tokens: environment restriction not enforced | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.15s) |
| backend | tokens: workspace of the token not checked (environment routes) | CAUGHT | --- FAIL: TestATokenOfOneWorkspaceReachesNothingOfAnother (0.15s) |
| backend | tokens: workspace of the token not checked (workspace routes) | CAUGHT | --- FAIL: TestATokenOfOneWorkspaceReachesNothingOfAnother (0.14s) |
| backend | tokens: restricted tokens may change workspace-level things | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.23s) |
| backend | tokens: expiry not checked for a cached token | CAUGHT | --- FAIL: TestExpiryAndTheCacheBound (0.14s) |
| backend | tokens: expiry not checked at lookup | CAUGHT | --- FAIL: TestMalformedTokensAreAllTheSameGenericRefusal (0.12s) |
| backend | tokens: a revoked token is accepted | CAUGHT | --- FAIL: TestTokensCannotManageTokensAndAdminsCan (0.22s) |
| backend | tokens: the cache TTL is ignored | CAUGHT | --- FAIL: TestExpiryAndTheCacheBound (0.13s) |
| backend | tokens: a revocation is not announced | CAUGHT | --- FAIL: TestRevocationReachesOtherInstances (6.29s) |
| backend | tokens: a revocation is not dropped locally | CAUGHT | --- FAIL: TestRevocationIsImmediateOnTheInstanceThatHandledItEvenWithoutABroadcast (0.13s) |
| backend | tokens: last_used written on every request | CAUGHT | --- FAIL: TestLastUsedIsWrittenAtMostOncePerMinute (0.16s) |
| backend | tokens: failed attempts are checked after the work, not before | CAUGHT | --- FAIL: TestFailedTokenAttemptsAreLimitedPerIPBeforeAnyHashingOrDatabaseWork (0.13s) |
| backend | tokens: failures are not counted | CAUGHT | --- FAIL: TestFailedTokenAttemptsAreLimitedPerIPBeforeAnyHashingOrDatabaseWork (0.12s) |
| backend | tokens: no per-token rate limit | CAUGHT | --- FAIL: TestEachTokenHasItsOwnRateLimitAndTheCreatorKeepsTheirs (0.11s) |
| backend | tokens: a token's traffic counts against its creator | CAUGHT | --- FAIL: TestATokensTrafficDoesNotThrottleItsCreator (0.13s) |
| backend | tokens: the audit actor is not taken from the token | CAUGHT | --- FAIL: TestAuditEntriesShowTheTokenAsTheActor (0.15s) |
| backend | tokens: no cap on tokens per person | CAUGHT | --- FAIL: TestMaximumActiveTokensQuota (0.13s) |
| backend | tokens: no workspace lock on the token cap (a race) | CAUGHT | --- FAIL: TestTokenCapHoldsUnderConcurrency (0.24s) |
| backend | tokens: a token may live longer than a year | CAUGHT | --- FAIL: TestTokensCannotManageTokensAndAdminsCan (0.15s) |
| backend | tokens: the credential is also read from the query string | CAUGHT | --- FAIL: TestATokenIsOnlyAcceptedInTheAuthorizationBearerHeader (0.13s) |
| backend | tokens: an expired token gets a different answer | CAUGHT | --- FAIL: TestExpiryAndTheCacheBound (0.12s) |
| backend | tokens: the hash is compared with == | CAUGHT | --- FAIL: TestTokenCodeUsesConstantTimeComparisonAndOnlyTheHeader (0.00s) |
| backend | tokens: a bad token is written to the log | CAUGHT | --- FAIL: TestTokenCodeUsesConstantTimeComparisonAndOnlyTheHeader (0.00s) |
| backend | tokens: a restricted token can promote from another environment | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.14s) |
| backend | tokens: segment usage reveals other environments to a restricted token | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.16s) |
| backend | tokens: environments listing unfiltered | CAUGHT | --- FAIL: TestATokenLimitedToEnvironmentsSeesNoOthers (0.15s) |
| backend | scheduled by token: revoking does not cancel the token's pending changes | CAUGHT | --- FAIL: TestRevokingATokenCancelsItsPendingScheduledChanges (0.13s) |
| backend | scheduled by token: the executor runs a change whose token is gone | CAUGHT | --- FAIL: TestTheExecutorRefusesAChangeWhoseTokenIsGoneEvenIfTheCancelWasMissed (0.15s) |
| backend | scheduled by token: expired tokens' pending changes are not swept | CAUGHT | --- FAIL: TestAnExpiredTokensPendingChangesAreCancelledBeforeTheyAreDue (0.13s) |
| backend | scheduled by token: the creating token is not recorded | CAUGHT | --- FAIL: TestRevokingATokenCancelsItsPendingScheduledChanges (0.13s) |
| backend | openapi: a route allowed for tokens but unmarked in the spec | CAUGHT | --- FAIL: TestOpenAPITokenMarksMatchTheRouterRegistry (0.15s) |
| backend | openapi: a spec operation marked token-accessible that the router denies | CAUGHT | --- FAIL: TestOpenAPITokenMarksMatchTheRouterRegistry (0.12s) |
| backend | outbox: a claimed row is claimable again by a second worker (candidate snapshot only) | CAUGHT | --- FAIL: TestTwoWorkersNeverClaimTheSameRow (0.18s) |
| backend | outbox: a result is recorded without holding the lease | CAUGHT | --- FAIL: TestAStaleLeaseIsReclaimedAndTheOldWorkerCannotRecordAResult (0.17s) |
| backend | outbox: the network call is made inside a transaction | CAUGHT | --- FAIL: TestTheNetworkCallHappensOutsideAnyTransaction (0.16s) |
| backend | outbox: an expired lease is never reclaimed | CAUGHT | --- FAIL: TestAStaleLeaseIsReclaimedAndTheOldWorkerCannotRecordAResult (0.16s) |
| backend | outbox: backoff does not grow | CAUGHT | --- FAIL: TestBackoffThenDeadAfterTheLastAttempt (0.13s) |
| backend | outbox: a row is never dead-lettered | CAUGHT | --- FAIL: TestBackoffThenDeadAfterTheLastAttempt (0.14s) |
| backend | outbox: a row that keeps killing its worker is retried forever | CAUGHT | --- FAIL: TestAnOutboxRowThatKeepsKillingItsWorkerStopsAtTheBound (0.13s) |
| backend | outbox: no per-target in-flight cap | CAUGHT | --- FAIL: TestOneSlowTargetCannotTakeEveryWorker (0.13s) |
| backend | outbox: in-flight rows of a target are not counted across workers | CAUGHT | --- FAIL: TestOneSlowTargetCannotTakeEveryWorker (0.13s) |
| backend | outbox: a full queue is not capped | CAUGHT | --- FAIL: TestTheQueueCapDropsTheOldestWebhooksAndNeverBlocksTheChange (0.17s) |
| backend | outbox: the overflow is not audited once | CAUGHT | --- FAIL: TestTheQueueCapDropsTheOldestWebhooksAndNeverBlocksTheChange (0.14s) |
| backend | outbox: delivered rows are kept forever | CAUGHT | --- FAIL: TestPurgeRemovesOnlyOldFinishedRows (0.13s) |
| backend | outbox: pending rows can be purged | CAUGHT | --- FAIL: TestPurgeRemovesOnlyOldFinishedRows (0.22s) |
| backend | outbox: stored errors are not sanitised | CAUGHT | --- FAIL: TestErrorsAreStoredShortAndPrintable (0.12s) |
| backend | outbox: the secret-header check for the internal endpoint is skipped | CAUGHT | --- FAIL: TestOutboxEndpointNeedsTheSecret (0.08s) |
| backend | webhooks: the signature does not cover the delivery id | CAUGHT | --- FAIL: TestGoldenSignatureVector (0.00s) |
| backend | webhooks: the signature does not cover the timestamp | CAUGHT | --- FAIL: TestGoldenSignatureVector (0.00s) |
| backend | webhooks: the reference verifier ignores the timestamp tolerance | CAUGHT | --- FAIL: TestVerifyAcceptsTheRightSignatureWithinTheTolerance (0.00s) |
| backend | webhooks: the reference verifier accepts only the first signature | CAUGHT | --- FAIL: TestARotationHeaderVerifiesWithEitherSecret (0.00s) |
| backend | webhooks: during a rotation only one signature is sent | CAUGHT | --- FAIL: TestSecretRotationSendsBothSignaturesDuringTheGrace (0.14s) |
| backend | webhooks: the old secret signs forever | CAUGHT | --- FAIL: TestSecretRotationSendsBothSignaturesDuringTheGrace (0.14s) |
| backend | webhooks: the delivery id changes on every attempt | CAUGHT | --- FAIL: TestADeliveryIsSignedAndCarriesAStableDeliveryID (0.17s) |
| backend | webhooks: creation works without an encryption key | CAUGHT | --- FAIL: TestWithoutAKeyWebhooksFailClosed (0.13s) |
| backend | webhooks: viewers may manage endpoints | CAUGHT | --- FAIL: TestManagementNeedsAnAdminAndASessionAndNeverShowsTheSecretAgain (0.15s) |
| backend | webhooks: API tokens may manage endpoints | CAUGHT | --- FAIL: TestOpenAPITokenMarksMatchTheRouterRegistry (0.12s) |
| backend | webhooks: another workspace can delete an endpoint | CAUGHT | --- FAIL: TestAnEndpointOnlyReceivesItsOwnWorkspacesEvents (0.13s) |
| backend | webhooks: events go to every workspace's endpoints | CAUGHT | --- FAIL: TestAnEndpointOnlyReceivesItsOwnWorkspacesEvents (0.13s) |
| backend | webhooks: the environment filter is ignored | CAUGHT | --- FAIL: TestEventTypeAndEnvironmentFiltersApply (0.15s) |
| backend | webhooks: the event type filter is ignored | CAUGHT | --- FAIL: TestEventTypeAndEnvironmentFiltersApply (0.13s) |
| backend | webhooks: a disabled endpoint still gets events queued | CAUGHT | --- FAIL: TestAnEndpointThatKeepsFailingIsDisabledAndAuditedAndCanBeReEnabled (0.22s) |
| backend | webhooks: an endpoint is never disabled automatically | CAUGHT | --- FAIL: TestAnEndpointThatKeepsFailingIsDisabledAndAuditedAndCanBeReEnabled (0.42s) |
| backend | webhooks: a test ping counts toward health | CAUGHT | --- FAIL: TestATestPingAndAResendAreSentAtOnceWithTheSameDeliveryID (0.13s) |
| backend | webhooks: a promotion also sends flag.updated | CAUGHT | --- FAIL: TestEveryEventIsEmittedWithTheDocumentedShapeAndNothingSecret (0.16s) |
| backend | webhooks: targeting rule contents travel in flag.updated | CAUGHT | --- FAIL: TestEveryEventIsEmittedWithTheDocumentedShapeAndNothingSecret (0.14s) |
| backend | webhooks: an API token's edit is attributed to the person | CAUGHT | --- FAIL: TestEveryEventIsEmittedWithTheDocumentedShapeAndNothingSecret (0.15s) |
| backend | webhooks: scheduled change outcomes are not announced | CAUGHT | --- FAIL: TestEveryEventIsEmittedWithTheDocumentedShapeAndNothingSecret (0.20s) |
| backend | webhooks: the flag edit event is not queued with the edit | CAUGHT | --- FAIL: TestADeliveryIsSignedAndCarriesAStableDeliveryID (0.14s) |
| backend | webhooks: the endpoint's URL is checked only syntactically at creation | CAUGHT | --- FAIL: TestHostileEndpointURLsAreRefusedAtCreationAndNothingIsStored (0.14s) |
| backend | webhooks: a failed delivery's stored error names the URL | CAUGHT | --- FAIL: TestNeitherTheSecretNorTheURLReachesTheLogs (0.13s) |
| backend | webhooks: a successful receiver's body is kept | CAUGHT | --- FAIL: TestADeliveryIsSignedAndCarriesAStableDeliveryID (0.16s) |
| backend | webhooks: the signing secret is stored in the clear | CAUGHT | --- FAIL: TestManagementNeedsAnAdminAndASessionAndNeverShowsTheSecretAgain (0.14s) |
| backend | ssrf: a URL with credentials is accepted | CAUGHT | --- FAIL: TestUnusualEncodingsAndFormsAreRefused (0.00s) |
| backend | ssrf: numeric spellings of an address are accepted | CAUGHT | --- FAIL: TestUnusualEncodingsAndFormsAreRefused (0.00s) |
| backend | ssrf: plain http is accepted | CAUGHT | --- FAIL: TestUnusualEncodingsAndFormsAreRefused (0.00s) |
| backend | ssrf: any port is accepted | CAUGHT | --- FAIL: TestUnusualEncodingsAndFormsAreRefused (0.00s) |
| backend | ssrf: localhost is accepted | CAUGHT | --- FAIL: TestUnusualEncodingsAndFormsAreRefused (0.00s) |
| backend | ssrf: IPv4-mapped IPv6 addresses are judged as IPv6 | CAUGHT | --- FAIL: TestEveryBlockedRangeIsRefusedAsALiteral (0.00s) |
| backend | ssrf: 6to4 addresses embedding a private IPv4 are accepted | CAUGHT | --- FAIL: TestEveryBlockedRangeIsRefusedAsALiteral (0.00s) |
| backend | ssrf: the cloud metadata address is accepted | CAUGHT | --- FAIL: TestEveryBlockedRangeIsRefusedAsALiteral (0.00s) |
| backend | ssrf: RFC 1918 private ranges are accepted | CAUGHT | --- FAIL: TestEveryBlockedRangeIsRefusedAsALiteral (0.00s) |
| backend | ssrf: carrier-grade NAT addresses are accepted | CAUGHT | --- FAIL: TestEveryBlockedRangeIsRefusedAsALiteral (0.00s) |
| backend | ssrf: unique-local IPv6 addresses are accepted | CAUGHT | --- FAIL: TestEveryBlockedRangeIsRefusedAsALiteral (0.00s) |
| backend | ssrf: a name resolving partly inside is accepted (only the first record is checked) | CAUGHT | --- FAIL: TestAMixedAnswerIsRefusedWhole (0.00s) |
| backend | ssrf: the connect-time hook does not check the address | CAUGHT | --- FAIL: TestTheDialerControlHookChecksTheAddressActuallyConnected (0.00s) |
| backend | ssrf: redirects are followed | CAUGHT | --- FAIL: TestRedirectsAreNotFollowed (0.00s) |
| backend | ssrf: the environment proxy is used | CAUGHT | --- FAIL: TestTheClientDoesNotUseAnEnvironmentProxy (0.00s) |
| backend | secrets: a signing secret is encrypted with a fixed nonce | CAUGHT | --- FAIL: TestSealOpenRoundTripAndFreshNonces (0.00s) |
| backend | secrets: ciphertext is not bound to its row | CAUGHT | --- FAIL: TestACiphertextIsBoundToItsRowAndRejectsTampering (0.00s) |
| backend | secrets: a sealed value under an unknown key is opened with the current one | CAUGHT | --- FAIL: TestRotationOldKeysStillOpenNewSealsUseTheCurrent (0.00s) |
| backend | email: the subject is not cleaned or encoded | CAUGHT | --- FAIL: TestHostileSubjectsNeverAddAHeader (0.00s) |
| backend | email: bodies are not quoted-printable | CAUGHT | --- FAIL: TestBodiesAreQuotedPrintableMultipartAndRoundTrip (0.00s) |
| backend | email: the sink logs the full recipient | CAUGHT | --- FAIL: TestTheSinkSendsNothingAndLogsOnlyARedactedLine (0.00s) |
| backend | email: the SMTP client sends over a connection with no TLS | CAUGHT | --- FAIL: TestSMTPNeverDowngradesStartTLS (0.00s) |
| backend | email: plaintext SMTP needs no development flag | CAUGHT | --- FAIL: TestSMTPRefusesPlaintextUnlessTheDevelopmentFlagIsOn (0.00s) |
| backend | email: names are not HTML-escaped | CAUGHT | --- FAIL: TestComposeEscapesEveryUserSuppliedFieldInTheHTML (0.00s) |
| backend | email: the invite token stays in the outbox after delivery | CAUGHT | --- FAIL: TestInviteEmailIsQueuedInTheSameTransactionAndSentOnce (0.16s) |
| backend | email: a revoked invite is still emailed | CAUGHT | --- FAIL: TestARevokedOrAcceptedInviteIsNotEmailed (0.14s) |
| backend | email: a resend neither retires the queued email nor is the old token refused at send time | CAUGHT | --- FAIL: TestAnEmailStillQueuedWhenAResendHappensIsRetiredNotSentWithTheOldToken (0.13s) |
| backend | email: a resend keeps the old link alive | CAUGHT | --- FAIL: TestResendIssuesANewLinkAndRetiresTheOldOne (0.14s) |
| backend | email: no cooldown between resends | CAUGHT | --- FAIL: TestResendIssuesANewLinkAndRetiresTheOldOne (0.12s) |
| backend | email: no per-address daily limit in a workspace | CAUGHT | --- FAIL: TestInviteEmailLimits (0.28s) |
| backend | email: no per-workspace daily limit | CAUGHT | --- FAIL: TestInviteEmailLimits (0.27s) |
| backend | email: the cross-workspace limit is reported as an error | CAUGHT | --- FAIL: TestThePerAddressLimitAcrossWorkspacesIsSilentToTheCaller (0.29s) |
| backend | email: the cross-workspace limit is not applied | CAUGHT | --- FAIL: TestThePerAddressLimitAcrossWorkspacesIsSilentToTheCaller (0.14s) |
| backend | email: the default mail sender sends for real | CAUGHT | --- FAIL: TestNothingConfiguredMeansTheSinkAndNoWebhooks (0.00s) |
| backend | ownership: the member rows are not locked, so a concurrent demotion is not serialised | CAUGHT | --- FAIL: TestAConcurrentDemotionDuringATransferNeverLeavesNoOwner (0.24s) |
| backend | ownership: acceptance does not check the target is still an admin | CAUGHT | --- FAIL: TestAcceptanceChecksEverythingAgain (0.13s) |
| backend | ownership: acceptance does not check the offerer is still an owner | CAUGHT | --- FAIL: TestAcceptanceChecksEverythingAgain (0.15s) |
| backend | ownership: an expired offer can be accepted | CAUGHT | --- FAIL: TestAnOfferExpiresAndIsAudited (0.15s) |
| backend | ownership: an expired offer is never marked or audited | CAUGHT | --- FAIL: TestAnOfferExpiresAndIsAudited (0.14s) |
| backend | ownership: the owned-workspaces limit is not applied to the new owner | CAUGHT | --- FAIL: TestTheOwnedWorkspacesLimitAppliesToTheNewOwner (0.22s) |
| backend | ownership: workspaces.owner_id does not move with the ownership | CAUGHT | --- FAIL: TestAnOwnerHandsAWorkspaceToAnAdmin (0.23s) |
| backend | ownership: the previous owner stays an owner | CAUGHT | --- FAIL: TestAnOwnerHandsAWorkspaceToAnAdmin (0.22s) |
| backend | ownership: an offer can be made to someone who is not an admin | CAUGHT | --- FAIL: TestOnlyAnOwnerOffersAndOnlyToAnAdmin (0.14s) |
| backend | ownership: an admin can offer (neither the route nor the service insists on an owner) | CAUGHT | --- FAIL: TestOnlyAnOwnerOffersAndOnlyToAnAdmin (0.15s) |
| backend | ownership: a second open offer is not told apart from a server error | CAUGHT | --- FAIL: TestOnlyAnOwnerOffersAndOnlyToAnAdmin (0.14s) |
| backend | ownership: someone else can accept an offer | CAUGHT | --- FAIL: TestOnlyAnOwnerOffersAndOnlyToAnAdmin (0.24s) |
| backend | ownership: API tokens may offer ownership | CAUGHT | --- FAIL: TestOpenAPITokenMarksMatchTheRouterRegistry (0.12s) |
| backend | deletion: environment routes still resolve a workspace scheduled for deletion | CAUGHT | --- FAIL: TestEveryWorkspaceRouteAnswersNotFoundOnceTheWorkspaceIsScheduledForDeletion (0.21s) |
| backend | deletion: workspace routes still resolve a workspace scheduled for deletion | CAUGHT | --- FAIL: TestEveryWorkspaceRouteAnswersNotFoundOnceTheWorkspaceIsScheduledForDeletion (0.20s) |
| backend | deletion: /me still lists a workspace scheduled for deletion | CAUGHT | --- FAIL: TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion (0.25s) |
| backend | deletion: a user whose only workspace is pending deletion gets a new personal one | CAUGHT | --- FAIL: TestAUserWhoseOnlyWorkspaceIsNotPersonalIsNotGivenANewOne (0.12s) |
| backend | deletion: the SDK key of a deleted workspace still verifies | CAUGHT | --- FAIL: TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion (0.24s) |
| backend | deletion: an open stream's re-check ignores a deleted workspace | CAUGHT | --- FAIL: TestWhenTheBroadcastIsLostTheStreamsRecheckStillEndsThem (3.16s) |
| backend | deletion: this instance keeps the workspace's keys cached | CAUGHT | --- FAIL: TestDropsTheKeysAtOnceEvenIfTheBroadcastNeverArrives (3.18s) |
| backend | deletion: other instances are not told | CAUGHT | --- FAIL: TestAnotherInstanceDropsTheKeyAndTheStreamPromptly (3.25s) |
| backend | deletion: open streams of the workspace are not woken | CAUGHT | --- FAIL: TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion (3.15s) |
| backend | deletion: the scheduler still claims a deleted workspace's changes | CAUGHT | --- FAIL: TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion (0.16s) |
| backend | deletion: the outbox still sends for a deleted workspace | CAUGHT | --- FAIL: TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion (5.27s) |
| backend | deletion: an invite to a deleted workspace can be accepted by link | CAUGHT | --- FAIL: TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion (0.21s) |
| backend | deletion: a token of a deleted workspace is still recorded as used | CAUGHT | --- FAIL: TestRestoringWithinTheGraceBringsEverythingBackExactly (0.20s) |
| backend | deletion: the workspace name is not required | CAUGHT | --- FAIL: TestOnlyAnOwnerSchedulesDeletionAfterTypingTheName (0.13s) |
| backend | deletion: an admin can delete (neither the route nor the service insists on an owner) | CAUGHT | --- FAIL: TestOnlyAnOwnerSchedulesDeletionAfterTypingTheName (0.24s) |
| backend | deletion: API tokens may delete a workspace | CAUGHT | --- FAIL: TestOpenAPITokenMarksMatchTheRouterRegistry (0.13s) |
| backend | deletion: restore works after the grace period | CAUGHT | --- FAIL: TestRestoreIsRefusedOnceTheGraceIsOverAndForAWorkspaceThatIsNotDeleted (0.26s) |
| backend | deletion: any member can restore | CAUGHT | --- FAIL: TestRestoringWithinTheGraceBringsEverythingBackExactly (0.14s) |
| backend | purge: only the first batch of each table is deleted | CAUGHT | --- FAIL: TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone (0.36s) |
| backend | purge: a workspace is purged before its grace period is over (the query and the re-check) | CAUGHT | --- FAIL: TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone (0.17s) |
| backend | purge: no tombstone is kept | CAUGHT | --- FAIL: TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone (0.32s) |
| backend | purge: the audit log is not purged | CAUGHT | --- FAIL: TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone (0.21s) |
| backend | purge: no final audit entry | CAUGHT | --- FAIL: TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone (0.24s) |
| backend | purge: the audit log can be updated during a due purge | CAUGHT | --- FAIL: TestTheAuditGateIsNotOpenedByBeingDueByAWrongDeclarationOrByALeakedSetting (0.22s) |
| backend | purge: the endpoint accepts any secret | CAUGHT | --- FAIL: TestThePurgeEndpointNeedsTheSecretAndIsOffWithoutOne (0.32s) |
| backend | export: editors may export | CAUGHT | --- FAIL: TestOnlyAdminsAndOwnersExportAndNeverATokenOrAnotherTenant (0.14s) |
| backend | export: API tokens may export | CAUGHT | --- FAIL: TestOnlyAdminsAndOwnersExportAndNeverATokenOrAnotherTenant (0.19s) |
| backend | export: formulas are not neutralised | CAUGHT | --- FAIL: TestCSVInjectionIsNeutralisedAndQuotingSurvivesHostileContent (0.15s) |
| backend | export: tab, carriage return and newline are not treated as formula starts | CAUGHT | --- FAIL: TestCSVInjectionIsNeutralisedAndQuotingSurvivesHostileContent (0.27s) |
| backend | export: the 90 day cap is not enforced | CAUGHT | --- FAIL: TestExportFormatsHeadersRangesAndTheAuditOfTheExport (0.13s) |
| backend | export: the row cap is not enforced | CAUGHT | --- FAIL: TestExportRowCapAndBoundedBatchesAreHonest (0.13s) |
| backend | export: the export is not audited | CAUGHT | --- FAIL: TestExportFormatsHeadersRangesAndTheAuditOfTheExport (0.23s) |
| backend | export: other environments' entries are included | CAUGHT | --- FAIL: TestExportOnlyHoldsThisEnvironmentsEntriesAndTheWorkspaceWideOnes (0.26s) |
| backend | export: other workspaces' entries are included | CAUGHT | --- FAIL: TestExportOnlyHoldsThisEnvironmentsEntriesAndTheWorkspaceWideOnes (0.13s) |
| backend | export: batches repeat the last row | CAUGHT | --- FAIL: TestExportRowCapAndBoundedBatchesAreHonest (0.19s) |
| backend | export: secrets under secret-looking keys are exported | CAUGHT | --- FAIL: TestNoSecretLeavesThroughAnExport (0.24s) |
| backend | export: secret-shaped strings are exported | CAUGHT | --- FAIL: TestNoSecretLeavesThroughAnExport (0.23s) |
| backend | export: not rate limited | CAUGHT | --- FAIL: TestExportIsRateLimited (0.34s) |
| backend | history: other environments' entries appear | CAUGHT | --- FAIL: TestAFlagsHistoryShowsWhoChangedWhatAndWhen (0.29s) |
| backend | history: other flags' entries appear | CAUGHT | --- FAIL: TestAFlagsHistoryShowsWhoChangedWhatAndWhen (0.34s) |
| backend | history: an API token's edit is shown as a person's | CAUGHT | --- FAIL: TestAFlagsHistoryShowsWhoChangedWhatAndWhen (0.19s) |
| backend | history: a scheduled change is shown as a person's | CAUGHT | --- FAIL: TestAFlagsHistoryShowsWhoChangedWhatAndWhen (0.19s) |
| backend | history: unchanged fields are listed as changes | CAUGHT | --- FAIL: TestAFlagsHistoryShowsWhoChangedWhatAndWhen (0.22s) |
| backend | history: never more than the first page | CAUGHT | --- FAIL: TestHistoryIsPagedAndIsolated (0.26s) |
| backend | history: only admins can read it | CAUGHT | --- FAIL: TestAFlagsHistoryShowsWhoChangedWhatAndWhen (0.25s) |
| backend | history: an edit with no changes is listed with a null list of changes (the drawer crashed) | CAUGHT | --- FAIL: TestAnEditWithNoChangesHasAnEmptyListNotNull (0.15s) |
| backend | listen: LISTEN_ADDR does not win over PORT | CAUGHT | --- FAIL: TestListenAddressPrecedenceIsListenAddrThenPortThenTheDefault (0.00s) |
| backend | listen: PORT is ignored | CAUGHT | --- FAIL: TestListenAddressPrecedenceIsListenAddrThenPortThenTheDefault (0.00s) |
| backend | listen: a port of 0 or above 65535 is accepted | CAUGHT | --- FAIL: TestAWrongValueIsAnErrorNotAFallbackToTheNextOne (0.00s) |
| backend | listen: a bad LISTEN_ADDR falls back to PORT | CAUGHT | --- FAIL: TestAWrongValueIsAnErrorNotAFallbackToTheNextOne (0.00s) |
| backend | listen: any host text is accepted | CAUGHT | --- FAIL: TestAWrongValueIsAnErrorNotAFallbackToTheNextOne (0.00s) |
| backend | control: two controls are allowed | CAUGHT | --- FAIL: TestTheDatabaseAllowsAtMostOneControlPerExperiment (0.36s) |
| backend | control: the backfill picks the wrong variation | CAUGHT | --- FAIL: TestMigration0014BackfillsTheFirstVariationAsControl (0.12s) |
| backend | control: a control that is not the flag's variation is accepted | CAUGHT | --- FAIL: TestTheFirstVariationIsTheControlUnlessYouChooseAnother (0.19s) |
| backend | control: the default control is the last variation | CAUGHT | --- FAIL: TestTheFirstVariationIsTheControlUnlessYouChooseAnother (0.22s) |
| backend | control: it can change after exposures | CAUGHT | --- FAIL: TestTheControlIsChosenBeforeExposuresAndFrozenAfter (0.19s) |
| backend | control: results ignore the designated control | CAUGHT | --- FAIL: TestResultsUseTheDesignatedControlAndASwapIsTheMirrorImage (1.16s) |
| backend | control: viewers can change it | CAUGHT | --- FAIL: TestTheControlIsChosenBeforeExposuresAndFrozenAfter (0.34s) |
| backend | control: API tokens can change it | CAUGHT | --- FAIL: TestTheControlIsChosenBeforeExposuresAndFrozenAfter (0.13s) |
| backend | control: moving the control puts it nowhere | CAUGHT | --- FAIL: TestControlFirstMovesOnlyTheControl (0.00s) |
| backend | audit gate: no declaration is needed (being due is enough again) | CAUGHT | --- FAIL: TestTheAuditGateIsNotOpenedByBeingDueByAWrongDeclarationOrByALeakedSetting (0.18s) |
| backend | audit gate: the declaration is not bound to the transaction id | CAUGHT | --- FAIL: TestTheAuditGateIsNotOpenedByBeingDueByAWrongDeclarationOrByALeakedSetting (0.18s) |
| backend | audit gate: the declaration is not bound to the workspace | CAUGHT | --- FAIL: TestTheAuditGateIsNotOpenedByBeingDueByAWrongDeclarationOrByALeakedSetting (0.14s) |
| backend | audit gate: a workspace that is not due can be purged | CAUGHT | --- FAIL: TestTheAuditGateIsNotOpenedByBeingDueByAWrongDeclarationOrByALeakedSetting (0.15s) |
| backend | audit gate: the purge does not declare itself | CAUGHT | --- FAIL: TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone (0.15s) |
| backend | dead end: the owned-workspaces message does not say pending deletions count | CAUGHT | --- FAIL: TestAPendingDeletionStillCountsTowardTheOwnedWorkspacesLimitUntilPurged (0.26s) |
| backend | dead end: the owned-workspaces limit forgets pending deletions | CAUGHT | --- FAIL: TestAPendingDeletionStillCountsTowardTheOwnedWorkspacesLimitUntilPurged (0.13s) |
| backend | local stack: a data directory inside the repository is accepted | CAUGHT | --- FAIL: TestTheLocalStackRefusesDataInsideTheRepoAndAnythingThatIsNotLocal (1.00s) |
| backend | local stack: a non-loopback LISTEN_ADDR is accepted | CAUGHT | --- FAIL: TestTheLocalStackRefusesDataInsideTheRepoAndAnythingThatIsNotLocal (0.03s) |
| backend | local stack: a remote database host is accepted | CAUGHT | --- FAIL: TestTheLocalStackRefusesDataInsideTheRepoAndAnythingThatIsNotLocal (0.06s) |
| backend | cache: a change made on this instance does not drop this instance's entry | CAUGHT | --- FAIL: TestTheInvalidatingPublisherDropsLocallyBeforeAndEvenWhenTheBrokerFails (0.00s) |
| backend | cache: flag-channel messages from other instances are ignored | CAUGHT | --- FAIL: TestAnEditReachesTheOtherInstanceWithinMillisecondsAndThisOneAtOnce (2.88s) |
| backend | cache: entries never expire (the TTL fallback is gone) | CAUGHT | --- FAIL: TestServedFromMemoryUntilTheTTLEnds (0.00s) |
| backend | cache: a load that began before an invalidation is stored | CAUGHT | --- FAIL: TestALoadStartedBeforeAnInvalidationWithNoVersionIsNeverStored (0.00s) |
| backend | cache: a request after an invalidation joins the load that began before it | CAUGHT | tests failed |
| backend | cache: the announced version is not a floor | CAUGHT | --- FAIL: TestASnapshotBelowTheAnnouncedVersionIsNotStored (0.00s) |
| backend | cache: an older snapshot replaces a newer one | CAUGHT | --- FAIL: TestAnOlderSnapshotNeverReplacesANewerOne (0.00s) |
| backend | cache: concurrent misses each read the database (no single loader) | CAUGHT | --- FAIL: TestConcurrentMissesShareOneLoad (0.05s) |
| backend | cache: an environment with no flags is cached for the full TTL | CAUGHT | --- FAIL: TestAnEnvironmentWithNoFlagsIsCachedBrieflyOnly (0.00s) |
| backend | cache: the size bound is not enforced | CAUGHT | --- FAIL: TestMemoryIsBoundedByEntriesAndBytes (0.00s) |
| backend | cache: a deleted workspace is not dropped from this instance's cache | CAUGHT | --- FAIL: TestTheInstanceThatTookAChangeDropsItsOwnEntriesEvenWhenItHearsNothingFromTheBroker (0.74s) |
| backend | cache: a workspace drop announced by another instance is ignored | CAUGHT | --- FAIL: TestRunHearsOtherInstancesThroughOnePatternSubscription (2.11s) |
| backend | cache: the loader reads a workspace scheduled for deletion | CAUGHT | --- FAIL: TestTheSnapshotLoaderReadsNothingForAWorkspaceScheduledForDeletion (0.14s) |
| backend | cache: the loader does not confine itself to the workspace | CAUGHT | --- FAIL: TestTheSnapshotLoaderReadsNothingForAWorkspaceScheduledForDeletion (0.12s) |
| backend | cache: a kill is not published | CAUGHT | --- FAIL: TestAKillIsFastEverywhereAndNeverWaitsForTheTTL (2.74s) |
| backend | cache: starting an experiment is not published | CAUGHT | --- FAIL: TestStartingAndStoppingAnExperimentReachesTheOtherInstance (2.71s) |
| backend | cache: stopping an experiment is not published | CAUGHT | --- FAIL: TestStartingAndStoppingAnExperimentReachesTheOtherInstance (2.67s) |
| backend | cache: a promotion is not published | CAUGHT | --- FAIL: TestAPromotedConfigurationReachesTheOtherInstance (2.73s) |
| backend | apikey: every concurrent miss runs its own Argon2id hash | CAUGHT | --- FAIL: TestConcurrentMissesForOneKeyVerifyItOnce (0.06s) |
| backend | apikey: a verification that began before a revocation is cached | CAUGHT | --- FAIL: TestARevocationDuringVerificationIsNotUndone (0.00s) |
| backend | apikey: a request after a revocation joins the verification that began before it | CAUGHT | tests failed |
| backend | apikey: invalidating a key does not count as an invalidation | CAUGHT | tests failed |
| backend | track: no bound on concurrent writers | CAUGHT | --- FAIL: TestWhenTheDatabaseIsSaturatedBatchesAreRefusedFastAndNothingIsHalfWritten (1.23s) |
| backend | track: a refused batch has no Retry-After | CAUGHT | --- FAIL: TestWhenTheDatabaseIsSaturatedBatchesAreRefusedFastAndNothingIsHalfWritten (5.80s) |
| backend | track: a refused batch keeps its rate-limit tokens | CAUGHT | --- FAIL: TestARefusedBatchIsRefundedItsRateLimitTokens (1.08s) |
| backend | track: the wait for a turn is effectively unlimited | CAUGHT | --- FAIL: TestWhenTheDatabaseIsSaturatedBatchesAreRefusedFastAndNothingIsHalfWritten (3.19s) |
| backend | track: the gate lets everyone in | CAUGHT | --- FAIL: TestWhenTheDatabaseIsSaturatedBatchesAreRefusedFastAndNothingIsHalfWritten (1.19s) |
| backend | track: events are acknowledged before they are written | CAUGHT | --- FAIL: TestAnEventIsNotAcknowledgedBeforeItIsStored (0.51s) |
| backend | health: readiness does not flip when the server is shutting down | CAUGHT | --- FAIL: TestShuttingDownIsNotReadyAtOnce (0.00s) |
| backend | health: a database that is down is still reported ready | CAUGHT | --- FAIL: TestReadinessSaysNotReadyWhenTheDatabaseIsDownWithoutLeakingAnything (0.00s) |
| backend | health: Redis down makes the instance unready even when it is not required | CAUGHT | --- FAIL: TestRedisDownIsDegradedNotUnreadyUnlessRequired (0.01s) |
| backend | health: the readiness response quotes the error | CAUGHT | --- FAIL: TestRedisDownIsDegradedNotUnreadyUnlessRequired (0.00s) |
| backend | shutdown: new streams are accepted while the server is draining | CAUGHT | --- FAIL: TestSIGTERMFlipsReadinessFirstTellsStreamsToReconnectAndDrainsRequests (4.25s) |
| backend | shutdown: open streams are not told to reconnect | CAUGHT | --- FAIL: TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget (1.90s) |
| backend | shutdown: open streams ignore the drain | CAUGHT | --- FAIL: TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget (3.67s) |
| backend | shutdown: the drain is never started | CAUGHT | --- FAIL: TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget (2.90s) |
| backend | shutdown: the budget is not enforced | CAUGHT | --- FAIL: TestARequestThatOutlivesTheBudgetIsCutOffAndTheServerStillExitsOnTime (4.67s) |
| backend | shutdown: connections that never sent a request hold the shutdown | CAUGHT | --- FAIL: TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget (6.18s) |
| backend | shutdown: in-flight requests are cut off at once instead of drained | CAUGHT | --- FAIL: TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget (2.11s) |
| backend | shutdown: scheduler jobs claimed but not run are not released | CAUGHT | --- FAIL: TestScheduledChangesClaimedButNotRunWhenAskedToStopAreReleasedAtOnce (0.13s) |
| backend | shutdown: a scheduler release forgets to take the attempt off | CAUGHT | --- FAIL: TestScheduledChangesClaimedButNotRunWhenAskedToStopAreReleasedAtOnce (0.15s) |
| backend | shutdown: a scheduler release takes rows that belong to another worker | CAUGHT | --- FAIL: TestSchedulerReleasingNeverTouchesAChangeAnotherWorkerHasTakenOver (0.12s) |
| backend | shutdown: outbox rows claimed but not sent are not released | CAUGHT | --- FAIL: TestOutboxRowsClaimedButNotSentWhenAskedToStopAreReleasedAndASendUnderWayIsRecorded (0.19s) |
| backend | shutdown: outbox sends stop mid-way instead of finishing and recording | CAUGHT | --- FAIL: TestOutboxRowsClaimedButNotSentWhenAskedToStopAreReleasedAndASendUnderWayIsRecorded (0.18s) |
| backend | shutdown: an outbox release takes rows that belong to another worker | CAUGHT | --- FAIL: TestReleasingNeverTouchesARowAnotherWorkerHasTakenOver (0.13s) |
| backend | shutdown: the job endpoints do not stop when the server drains | CAUGHT | --- FAIL: TestDrainIsOnceAndEndsBoundContexts (1.00s) |
| backend | production: a development flag does not stop the start (webhooks to loopback) | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: rate limits may be off | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: email confirmation may be off | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: a per-process exposure token key is accepted | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: a database without TLS is accepted | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: the local default database is accepted | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: a wildcard CORS origin is accepted | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: an insecure auth server is accepted | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: unencrypted SMTP is accepted | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: a job is checked for settings it does not use / the API for fewer | CAUGHT | --- FAIL: TestEveryDevelopmentOnlySettingIsRefusedInProduction (0.00s) |
| backend | production: problems quote values | CAUGHT | --- FAIL: TestProblemsNameSettingsAndNeverQuoteValues (0.00s) |
| backend | production: a mistyped HELIOS_ENV selects development | CAUGHT | --- FAIL: TestHeliosEnvIsStrictAndDevelopmentIsTheDefault (0.00s) |
| backend | production: the process does not stop when a check fails | CAUGHT | --- FAIL: TestTheRealBinaryRefusesDevelopmentSettingsInProductionBeforeConnectingToAnything (1.01s) |
| backend | config: profiling may be exposed beyond this machine | CAUGHT | --- FAIL: TestPprofAddressMustBeLoopback (0.00s) |
| backend | db: the configured pool size is ignored | CAUGHT | --- FAIL: TestTheConfiguredPoolSizeIsTheOneTheDriverUses (0.01s) |
| backend | db: the default pool size is unbounded | CAUGHT | --- FAIL: TestDefaultPoolSizeFollowsTheCPUsBetweenFourAndTen (0.00s) |
| backend | runbook: a job directory is missing from the runbook | CAUGHT | --- FAIL: TestEveryCommandAndInternalRouteIsInTheRunbook (0.00s) |
| backend | runbook: an internal endpoint is missing from the runbook | CAUGHT | --- FAIL: TestEveryInternalRouteIsInTheRunbookWithItsSecretHeader (0.10s) |
| backend | runbook: the cadence of a job is not stated | CAUGHT | --- FAIL: TestEveryJobHasItsOperationalFacts (0.00s) |
| backend | image: the entrypoint can start something that is not in the table | CAUGHT | --- FAIL: TestUnknownCommandsExitNonZeroAndTheListIsPrinted (0.40s) |
| backend | image: a new cmd directory is not reachable from the image | CAUGHT | --- FAIL: TestTheCommandTableMatchesTheCmdDirectories (0.00s) |
| backend | image: the Dockerfile may run as root | CAUGHT | --- FAIL: TestTheDockerfileIsPinnedStaticNonRootAndCopiesOnlyTheSource (0.00s) |
| backend | image: the base image is not pinned to an exact version | CAUGHT | --- FAIL: TestTheDockerfileIsPinnedStaticNonRootAndCopiesOnlyTheSource (0.00s) |
| backend | image: the whole build context is copied | CAUGHT | --- FAIL: TestTheDockerfileIsPinnedStaticNonRootAndCopiesOnlyTheSource (0.00s) |
| backend | image: the final stage is not minimal | CAUGHT | --- FAIL: TestTheDockerfileIsPinnedStaticNonRootAndCopiesOnlyTheSource (0.00s) |
| backend | image: the ignore file lets .env into the build context | CAUGHT | --- FAIL: TestTheDockerignoreKeepsSecretsTestsAndTestDataOutOfTheBuildContext (0.00s) |
| backend | image: the ignore file lets tests and test data in | CAUGHT | --- FAIL: TestTheDockerignoreKeepsSecretsTestsAndTestDataOutOfTheBuildContext (0.00s) |
| backend | image: the check ignores environment files | CAUGHT | --- FAIL: TestEverythingThatMustNotShipIsFound (0.03s) |
| backend | image: the check ignores private keys inside files | CAUGHT | --- FAIL: TestEverythingThatMustNotShipIsFound (0.04s) |
| backend | image: the check allows files outside the allowed places | CAUGHT | --- FAIL: TestEverythingThatMustNotShipIsFound (0.04s) |
| backend | image: the check excuses any database password in a program | CAUGHT | --- FAIL: TestAPublishedDevelopmentDefaultAndStringTableNeighboursAreNotSecrets (0.00s) |
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
| frontend | webhooks ui: a delivered delivery can be resent | CAUGHT | Error: expect(received).toBe(expected) // Object.is equality |
| frontend | webhooks ui: a failing delivery does not say it will retry | CAUGHT | Error: expect(received).toMatch(expected) |
| frontend | webhooks ui: a disabled endpoint is not explained | CAUGHT | Error: expect(received).toContain(expected) // indexOf |
| frontend | webhooks ui: the colour is the only difference between states | CAUGHT | Error: expect(received).toBeGreaterThan(expected) |
| frontend | webhooks ui: the URL check accepts an empty URL | CAUGHT | TypeError: expect(received).toMatch(expected) |
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
| ui | webhooks ui: the secret dialog can be dismissed by accident | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | webhooks ui: Done works before the secret is stored | CAUGHT | Error: expect(locator).toBeDisabled() failed |
| ui | webhooks ui: members below admin are offered Add webhook | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | webhooks ui: delete does not ask first | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | webhooks ui: the dev mail sink is not disclosed | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | webhooks ui: an invite email's status is not shown | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | webhooks ui: a delivered delivery shows a resend button in the log | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | lifecycle ui: members who are not owners see the ownership controls | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | lifecycle ui: deleting needs no exact name | CAUGHT | Error: expect(locator).toBeDisabled() failed |
| ui | lifecycle ui: non-owners see the delete zone | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | lifecycle ui: scheduled-for-deletion workspaces are not listed for restore | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | lifecycle ui: viewers are offered the audit export | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | lifecycle ui: the history shows times without their zone | CAUGHT | Error: expect(locator).toHaveText(expected) failed |
| ui | lifecycle ui: the control can be changed after exposures | CAUGHT | Error: expect(locator).toHaveCount(expected) failed |
| ui | lifecycle ui: the ownership offer banner does not say what accepting does | CAUGHT | Error: expect(locator).toContainText(expected) failed |
| ui | dead end ui: a user with no workspace gets a blank console instead of the empty state | CAUGHT | Error: expect(locator).toBeVisible() failed |
| ui | dead end ui: the empty state offers no way to create a workspace | CAUGHT | Error: locator.fill: Test ended. |

package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

class RuleEngineTest {

    private val engine = engine()
    private val tuesdayEvening = at("2026-11-03T20:00")
    private val emptyUsage = usage("2026-11-03")

    private fun decide(
        pkg: String = NETFLIX,
        policy: Policy = ENFORCING,
        usage: UsageSnapshot = emptyUsage,
        grants: List<Grant> = emptyList(),
        now: java.time.Instant = tuesdayEvening,
        unreachableSince: java.time.Instant? = null,
    ) = engine.evaluate(pkg, policy, usage, grants, now, unreachableSince)

    // ---------- short circuits ----------

    @Test
    fun `exempt package is allowed even when every other rule would block it`() {
        val policy = ENFORCING.copy(
            listMode = ListMode.ALLOWLIST,
            allowlist = emptySet(),
            budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 0)),
            curfews = listOf(curfew(days = DayOfWeek.entries.toSet(), start = "00:00", end = "00:00")),
        )
        val decision = decide(pkg = LAUNCHER, policy = policy)
        assertEquals(Decision.Allow(AllowReason.EXEMPT), decision)
    }

    @Test
    fun `nothing is enforced while the parent switch is off`() {
        val policy = ENFORCING.copy(
            enforcementEnabled = false,
            listMode = ListMode.BLOCKLIST,
            blocklist = setOf(NETFLIX),
        )
        assertEquals(AllowReason.ENFORCEMENT_OFF, (decide(policy = policy) as Decision.Allow).reason)
    }

    @Test
    fun `an unconfigured install enforces nothing`() {
        val decision = decide(policy = Policy.UNCONFIGURED)
        assertTrue(decision.allowed)
    }

    // ---------- package lists ----------

    @Test
    fun `allowlist blocks anything not on it`() {
        val policy = ENFORCING.copy(listMode = ListMode.ALLOWLIST, allowlist = setOf(KHAN))
        assertEquals(BlockReason.NOT_IN_ALLOWLIST, (decide(policy = policy) as Decision.Block).reason)
        assertTrue(decide(pkg = KHAN, policy = policy).allowed)
    }

    @Test
    fun `blocklist blocks only what is on it`() {
        val policy = ENFORCING.copy(listMode = ListMode.BLOCKLIST, blocklist = setOf(NETFLIX))
        assertEquals(BlockReason.IN_BLOCKLIST, (decide(policy = policy) as Decision.Block).reason)
        assertTrue(decide(pkg = KHAN, policy = policy).allowed)
    }

    @Test
    fun `list mode OFF ignores both lists`() {
        val policy = ENFORCING.copy(
            listMode = ListMode.OFF,
            allowlist = setOf(KHAN),
            blocklist = setOf(NETFLIX),
        )
        assertTrue(decide(policy = policy).allowed)
    }

    // ---------- curfews ----------

    @Test
    fun `active curfew blocks and reports when it lifts`() {
        val policy = ENFORCING.copy(
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00")),
        )
        val block = decide(policy = policy) as Decision.Block
        assertEquals(BlockReason.CURFEW, block.reason)
        assertEquals(at("2026-11-03T21:00"), block.retryAt)
    }

    @Test
    fun `overlapping curfews block until the last one closes`() {
        val policy = ENFORCING.copy(
            curfews = listOf(
                curfew(id = "a", days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "20:30"),
                curfew(id = "b", days = setOf(DayOfWeek.TUESDAY), start = "19:30", end = "22:00"),
            ),
        )
        val block = decide(policy = policy) as Decision.Block
        assertEquals(at("2026-11-03T22:00"), block.retryAt)
    }

    @Test
    fun `a curfew scoped to other packages does not block this one`() {
        val policy = ENFORCING.copy(
            curfews = listOf(
                curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00", packages = setOf(YOUTUBE)),
            ),
        )
        assertTrue(decide(pkg = NETFLIX, policy = policy).allowed)
        assertTrue(!decide(pkg = YOUTUBE, policy = policy).allowed)
    }

    // ---------- budgets ----------

    @Test
    fun `daily budget blocks once it is spent and resets at local midnight`() {
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 60))
        val block = decide(policy = policy, usage = spent) as Decision.Block
        assertEquals(BlockReason.DAILY_BUDGET_EXHAUSTED, block.reason)
        assertEquals(60, block.limitMinutes)
        assertEquals(at("2026-11-04T00:00"), block.retryAt)
    }

    @Test
    fun `one minute short of the daily budget is still allowed`() {
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 59))
        val allow = decide(policy = policy, usage = spent) as Decision.Allow
        assertEquals(60L, allow.remaining.dailySeconds)
    }

    @Test
    fun `no daily limit configured for today means no daily limit`() {
        // Only Monday has a limit; today is a Tuesday.
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.MONDAY to 10)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 600))
        val allow = decide(policy = policy, usage = spent) as Decision.Allow
        assertNull(allow.remaining.dailySeconds)
    }

    @Test
    fun `time spent in exempt packages does not eat the budget`() {
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 50, GUARD to 30, LAUNCHER to 15))
        val allow = decide(policy = policy, usage = spent) as Decision.Allow
        assertEquals(10 * 60L, allow.remaining.dailySeconds)
    }

    @Test
    fun `per app budget blocks only the app that overspent`() {
        val policy = ENFORCING.copy(budgets = Budgets(perAppDailyMinutes = mapOf(NETFLIX to 30)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 30, KHAN to 200))
        assertEquals(
            BlockReason.APP_BUDGET_EXHAUSTED,
            (decide(pkg = NETFLIX, policy = policy, usage = spent) as Decision.Block).reason,
        )
        assertTrue(decide(pkg = KHAN, policy = policy, usage = spent).allowed)
    }

    @Test
    fun `weekly budget blocks and resets at the start of next week`() {
        val policy = ENFORCING.copy(budgets = Budgets(weeklyMinutes = 300))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 40), week = mapOf(NETFLIX to 300))
        val block = decide(policy = policy, usage = spent) as Decision.Block
        assertEquals(BlockReason.WEEKLY_BUDGET_EXHAUSTED, block.reason)
        assertEquals(at("2026-11-09T00:00"), block.retryAt)
    }

    @Test
    fun `per app limit is reported before the daily limit`() {
        val policy = ENFORCING.copy(
            budgets = Budgets(
                dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60),
                perAppDailyMinutes = mapOf(NETFLIX to 30),
            ),
        )
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 60))
        assertEquals(
            BlockReason.APP_BUDGET_EXHAUSTED,
            (decide(policy = policy, usage = spent) as Decision.Block).reason,
        )
    }

    // ---------- precedence ----------

    @Test
    fun `list is checked before curfew and curfew before budget`() {
        val policy = ENFORCING.copy(
            listMode = ListMode.ALLOWLIST,
            allowlist = emptySet(),
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00")),
            budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 1)),
        )
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 500))
        assertEquals(
            BlockReason.NOT_IN_ALLOWLIST,
            (decide(policy = policy, usage = spent) as Decision.Block).reason,
        )

        val withoutList = policy.copy(listMode = ListMode.OFF)
        assertEquals(
            BlockReason.CURFEW,
            (decide(policy = withoutList, usage = spent) as Decision.Block).reason,
        )
    }

    // ---------- grants: the "mum can always turn it on" path ----------

    @Test
    fun `an active grant overrides a curfew`() {
        val policy = ENFORCING.copy(
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00")),
        )
        val grants = listOf(grant(from = "2026-11-03T19:50", to = "2026-11-03T20:20"))
        val allow = decide(policy = policy, grants = grants) as Decision.Allow
        assertEquals(AllowReason.GRANT, allow.reason)
        assertEquals("g1", allow.grant?.id)
    }

    @Test
    fun `an active grant overrides a spent budget and reports no remaining limit`() {
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 300))
        val grants = listOf(grant(from = "2026-11-03T19:00", to = "2026-11-03T21:00"))
        val allow = decide(policy = policy, usage = spent, grants = grants) as Decision.Allow
        assertEquals(Remaining.UNLIMITED, allow.remaining)
    }

    @Test
    fun `an expired grant is ignored`() {
        val policy = ENFORCING.copy(listMode = ListMode.ALLOWLIST, allowlist = emptySet())
        val grants = listOf(grant(from = "2026-11-03T18:00", to = "2026-11-03T19:00"))
        assertIs<Decision.Block>(decide(policy = policy, grants = grants))
    }

    @Test
    fun `a grant not yet in effect is ignored`() {
        val policy = ENFORCING.copy(listMode = ListMode.ALLOWLIST, allowlist = emptySet())
        val grants = listOf(grant(from = "2026-11-03T21:00", to = "2026-11-03T22:00"))
        assertIs<Decision.Block>(decide(policy = policy, grants = grants))
    }

    @Test
    fun `a grant scoped to one app does not unlock another`() {
        val policy = ENFORCING.copy(listMode = ListMode.ALLOWLIST, allowlist = emptySet())
        val grants = listOf(
            grant(from = "2026-11-03T19:00", to = "2026-11-03T21:00", packages = setOf(YOUTUBE)),
        )
        assertTrue(decide(pkg = YOUTUBE, policy = policy, grants = grants).allowed)
        assertIs<Decision.Block>(decide(pkg = NETFLIX, policy = policy, grants = grants))
    }

    @Test
    fun `a grant can waive the budget but leave the curfew standing`() {
        val policy = ENFORCING.copy(
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00")),
            budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 10)),
        )
        val grants = listOf(
            grant(from = "2026-11-03T19:00", to = "2026-11-03T21:00", bypassCurfew = false),
        )
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 500))
        assertEquals(
            BlockReason.CURFEW,
            (decide(policy = policy, usage = spent, grants = grants) as Decision.Block).reason,
        )
    }

    @Test
    fun `the grant that lasts longest wins`() {
        val grants = listOf(
            grant(id = "short", from = "2026-11-03T19:00", to = "2026-11-03T20:10"),
            grant(id = "long", from = "2026-11-03T19:00", to = "2026-11-03T22:00"),
        )
        assertEquals("long", engine.strongestGrant(NETFLIX, grants, tuesdayEvening)?.id)
    }

    @Test
    fun `an offline code grant works exactly like a parent app grant`() {
        val policy = ENFORCING.copy(listMode = ListMode.ALLOWLIST, allowlist = emptySet())
        val grants = listOf(
            grant(from = "2026-11-03T19:50", to = "2026-11-03T20:20", source = GrantSource.OFFLINE_CODE),
        )
        assertTrue(decide(policy = policy, grants = grants).allowed)
    }

    // ---------- behaviour when the Pi is unreachable ----------

    @Test
    fun `KEEP_ENFORCING keeps applying the cached policy however long the Pi is away`() {
        val policy = ENFORCING.copy(
            listMode = ListMode.ALLOWLIST,
            allowlist = emptySet(),
            offlineBehavior = OfflineBehavior.KEEP_ENFORCING,
        )
        assertIs<Decision.Block>(decide(policy = policy, unreachableSince = at("2026-10-01T00:00")))
    }

    @Test
    fun `OPEN_AFTER_GRACE stops enforcing only once the grace period has elapsed`() {
        val policy = ENFORCING.copy(
            listMode = ListMode.ALLOWLIST,
            allowlist = emptySet(),
            offlineBehavior = OfflineBehavior.OPEN_AFTER_GRACE,
            offlineGraceMinutes = 60,
        )
        assertIs<Decision.Block>(decide(policy = policy, unreachableSince = at("2026-11-03T19:01")))
        val allow = decide(policy = policy, unreachableSince = at("2026-11-03T19:00")) as Decision.Allow
        assertEquals(AllowReason.OFFLINE_GRACE, allow.reason)
    }

    @Test
    fun `OPEN_AFTER_GRACE does nothing while the Pi is reachable`() {
        val policy = ENFORCING.copy(
            listMode = ListMode.ALLOWLIST,
            allowlist = emptySet(),
            offlineBehavior = OfflineBehavior.OPEN_AFTER_GRACE,
        )
        assertIs<Decision.Block>(decide(policy = policy, unreachableSince = null))
    }

    // ---------- nextBoundary: when to wake up again ----------

    @Test
    fun `nextBoundary returns the moment the budget runs out`() {
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60)))
        val spent = usage("2026-11-03", today = mapOf(NETFLIX to 50))
        assertEquals(
            at("2026-11-03T20:10"),
            engine.nextBoundary(policy, spent, emptyList(), tuesdayEvening, NETFLIX),
        )
    }

    @Test
    fun `nextBoundary returns the next curfew start when nothing is active`() {
        val policy = ENFORCING.copy(
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "21:30", end = "23:00")),
        )
        assertEquals(
            at("2026-11-03T21:30"),
            engine.nextBoundary(policy, emptyUsage, emptyList(), tuesdayEvening, NETFLIX),
        )
    }

    @Test
    fun `nextBoundary returns the end of a curfew that is already running`() {
        val policy = ENFORCING.copy(
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00")),
        )
        assertEquals(
            at("2026-11-03T21:00"),
            engine.nextBoundary(policy, emptyUsage, emptyList(), tuesdayEvening, NETFLIX),
        )
    }

    @Test
    fun `nextBoundary picks the soonest of grant expiry and curfew end`() {
        val policy = ENFORCING.copy(
            curfews = listOf(curfew(days = setOf(DayOfWeek.TUESDAY), start = "19:00", end = "21:00")),
        )
        val grants = listOf(grant(from = "2026-11-03T19:00", to = "2026-11-03T20:30"))
        assertEquals(
            at("2026-11-03T20:30"),
            engine.nextBoundary(policy, emptyUsage, grants, tuesdayEvening, NETFLIX),
        )
    }

    @Test
    fun `nextBoundary falls back to local midnight when only a daily budget is set`() {
        val policy = ENFORCING.copy(budgets = Budgets(dailyMinutesByDay = mapOf(DayOfWeek.TUESDAY to 60)))
        // No foreground app, so there is no burn-down deadline to compute.
        assertEquals(
            at("2026-11-04T00:00"),
            engine.nextBoundary(policy, emptyUsage, emptyList(), tuesdayEvening, null),
        )
    }

    @Test
    fun `nextBoundary is null when there is nothing scheduled to change`() {
        assertNull(engine.nextBoundary(ENFORCING, emptyUsage, emptyList(), tuesdayEvening, NETFLIX))
        assertNull(
            engine.nextBoundary(
                ENFORCING.copy(enforcementEnabled = false),
                emptyUsage,
                emptyList(),
                tuesdayEvening,
                NETFLIX,
            ),
        )
    }

    @Test
    fun `nextBoundary includes the offline grace deadline only when the Pi is away`() {
        val policy = ENFORCING.copy(
            offlineBehavior = OfflineBehavior.OPEN_AFTER_GRACE,
            offlineGraceMinutes = 30,
        )
        assertEquals(
            at("2026-11-03T20:20"),
            engine.nextBoundary(
                policy, emptyUsage, emptyList(), tuesdayEvening, NETFLIX,
                backendUnreachableSince = at("2026-11-03T19:50"),
            ),
        )
        assertNull(engine.nextBoundary(policy, emptyUsage, emptyList(), tuesdayEvening, NETFLIX))
    }
}

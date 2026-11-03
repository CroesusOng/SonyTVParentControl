package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import java.time.Duration
import java.time.Instant
import java.time.LocalDateTime
import java.time.ZoneId

/**
 * The single place that decides whether a package may be on screen right now.
 *
 * Pure and synchronous: no Android types, no I/O, no system clock. Everything it needs
 * is passed in, which is why the whole decision path is covered by plain JVM unit tests.
 */
class RuleEngine(
    private val zone: ZoneId,
    /** First day of the week for the weekly budget. */
    private val weekStartsOn: DayOfWeek = DayOfWeek.MONDAY,
) {

    fun evaluate(
        packageName: String,
        policy: Policy,
        usage: UsageSnapshot,
        grants: List<Grant> = emptyList(),
        now: Instant,
        backendUnreachableSince: Instant? = null,
    ): Decision {
        if (packageName in policy.exemptPackages) {
            return Decision.Allow(AllowReason.EXEMPT)
        }
        if (!policy.enforcementEnabled) {
            return Decision.Allow(AllowReason.ENFORCEMENT_OFF)
        }
        if (isPastOfflineGrace(policy, now, backendUnreachableSince)) {
            return Decision.Allow(AllowReason.OFFLINE_GRACE)
        }

        val grant = strongestGrant(packageName, grants, now)
        val local = LocalDateTime.ofInstant(now, zone)

        if (grant?.bypassList != true) {
            listVerdict(packageName, policy)?.let { return it }
        }
        if (grant?.bypassCurfew != true) {
            curfewVerdict(packageName, policy, local)?.let { return it }
        }
        if (grant?.bypassBudget != true) {
            budgetVerdict(packageName, policy, usage, local, now)?.let { return it }
        }

        val remaining = if (grant?.bypassBudget == true) {
            Remaining.UNLIMITED
        } else {
            remainingUnder(packageName, policy, usage, local)
        }
        return Decision.Allow(
            reason = if (grant != null) AllowReason.GRANT else AllowReason.WITHIN_LIMITS,
            remaining = remaining,
            grant = grant,
        )
    }

    /**
     * The next instant at which [evaluate] could return a different answer, so the
     * enforcement service can sleep until then instead of busy-polling.
     * Null means nothing scheduled is going to change (no budgets, no curfews, no grants).
     */
    fun nextBoundary(
        policy: Policy,
        usage: UsageSnapshot,
        grants: List<Grant> = emptyList(),
        now: Instant,
        foregroundPackage: String? = null,
        backendUnreachableSince: Instant? = null,
    ): Instant? {
        if (!policy.enforcementEnabled) return null
        val local = LocalDateTime.ofInstant(now, zone)
        val candidates = mutableListOf<Instant>()

        val hasBudgets = policy.budgets.dailyMinutesByDay.isNotEmpty() ||
            policy.budgets.weeklyMinutes != null ||
            policy.budgets.perAppDailyMinutes.isNotEmpty()
        if (hasBudgets) {
            candidates += nextLocalMidnight(local)
            policy.budgets.weeklyMinutes?.let { candidates += nextWeekStart(local) }
        }

        grants.filter { it.isActiveAt(now) }.forEach { candidates += it.expiresAt }

        policy.curfews.forEach { curfew ->
            val end = curfew.activeWindowEnd(local)
            if (end != null) {
                candidates += end.atZone(zone).toInstant()
            } else {
                curfew.nextStart(local)?.let { candidates += it.atZone(zone).toInstant() }
            }
        }

        if (foregroundPackage != null && foregroundPackage !in policy.exemptPackages) {
            val remaining = remainingUnder(foregroundPackage, policy, usage, local)
            remaining.tightestSeconds?.let { seconds ->
                if (seconds > 0) candidates += now.plusSeconds(seconds)
            }
        }

        if (policy.offlineBehavior == OfflineBehavior.OPEN_AFTER_GRACE && backendUnreachableSince != null) {
            // The moment enforcement is due to lapse is itself worth waking up for.
            candidates += backendUnreachableSince.plusSeconds(policy.offlineGraceMinutes * 60L)
        }

        return candidates.filter { it.isAfter(now) }.minOrNull()
    }

    /** Grants covering [packageName], collapsed into the one with the widest bypasses. */
    fun strongestGrant(packageName: String, grants: List<Grant>, now: Instant): Grant? {
        val applicable = grants.filter { it.isActiveAt(now) && it.scope.covers(packageName) }
        if (applicable.isEmpty()) return null
        // Latest expiry wins; ties broken by how much it waives.
        return applicable.maxWithOrNull(
            compareBy<Grant> { it.expiresAt }
                .thenBy { listOf(it.bypassList, it.bypassCurfew, it.bypassBudget).count { b -> b } },
        )
    }

    private fun isPastOfflineGrace(
        policy: Policy,
        now: Instant,
        backendUnreachableSince: Instant?,
    ): Boolean {
        if (policy.offlineBehavior != OfflineBehavior.OPEN_AFTER_GRACE) return false
        val since = backendUnreachableSince ?: return false
        val deadline = since.plusSeconds(policy.offlineGraceMinutes * 60L)
        return !now.isBefore(deadline)
    }

    private fun listVerdict(packageName: String, policy: Policy): Decision.Block? =
        when (policy.listMode) {
            ListMode.OFF -> null
            ListMode.ALLOWLIST ->
                if (packageName in policy.allowlist) null
                else Decision.Block(BlockReason.NOT_IN_ALLOWLIST)
            ListMode.BLOCKLIST ->
                if (packageName in policy.blocklist) Decision.Block(BlockReason.IN_BLOCKLIST)
                else null
        }

    private fun curfewVerdict(
        packageName: String,
        policy: Policy,
        local: LocalDateTime,
    ): Decision.Block? {
        val active = policy.curfews.filter {
            it.scope.covers(packageName) && it.isActiveAt(local)
        }
        if (active.isEmpty()) return null
        // Blocked until the last overlapping window closes, not the first.
        val latestEnd = active.mapNotNull { it.activeWindowEnd(local) }.maxOrNull()
        val longest = active.maxByOrNull { it.activeWindowEnd(local) ?: local } ?: active.first()
        return Decision.Block(
            reason = BlockReason.CURFEW,
            curfew = longest,
            retryAt = latestEnd?.atZone(zone)?.toInstant(),
        )
    }

    private fun budgetVerdict(
        packageName: String,
        policy: Policy,
        usage: UsageSnapshot,
        local: LocalDateTime,
        now: Instant,
    ): Decision.Block? {
        val midnight = nextLocalMidnight(local)

        policy.budgets.perAppDailyMinutes[packageName]?.let { limit ->
            val used = usage.todaySecondsFor(packageName)
            if (used >= limit * 60L) {
                return Decision.Block(
                    reason = BlockReason.APP_BUDGET_EXHAUSTED,
                    limitMinutes = limit,
                    usedSeconds = used,
                    retryAt = midnight,
                )
            }
        }

        policy.budgets.dailyLimitFor(local.dayOfWeek)?.let { limit ->
            val used = usage.todayCountableSeconds(policy.exemptPackages)
            if (used >= limit * 60L) {
                return Decision.Block(
                    reason = BlockReason.DAILY_BUDGET_EXHAUSTED,
                    limitMinutes = limit,
                    usedSeconds = used,
                    retryAt = midnight,
                )
            }
        }

        policy.budgets.weeklyMinutes?.let { limit ->
            val used = usage.weekCountableSeconds(policy.exemptPackages)
            if (used >= limit * 60L) {
                return Decision.Block(
                    reason = BlockReason.WEEKLY_BUDGET_EXHAUSTED,
                    limitMinutes = limit,
                    usedSeconds = used,
                    retryAt = nextWeekStart(local),
                )
            }
        }
        return null
    }

    private fun remainingUnder(
        packageName: String,
        policy: Policy,
        usage: UsageSnapshot,
        local: LocalDateTime,
    ): Remaining = Remaining(
        appSeconds = policy.budgets.perAppDailyMinutes[packageName]
            ?.let { (it * 60L - usage.todaySecondsFor(packageName)).coerceAtLeast(0) },
        dailySeconds = policy.budgets.dailyLimitFor(local.dayOfWeek)
            ?.let { (it * 60L - usage.todayCountableSeconds(policy.exemptPackages)).coerceAtLeast(0) },
        weeklySeconds = policy.budgets.weeklyMinutes
            ?.let { (it * 60L - usage.weekCountableSeconds(policy.exemptPackages)).coerceAtLeast(0) },
    )

    private fun nextLocalMidnight(local: LocalDateTime): Instant =
        local.toLocalDate().plusDays(1).atStartOfDay(zone).toInstant()

    private fun nextWeekStart(local: LocalDateTime): Instant {
        var date = local.toLocalDate().plusDays(1)
        while (date.dayOfWeek != weekStartsOn) {
            date = date.plusDays(1)
        }
        return date.atStartOfDay(zone).toInstant()
    }

    companion object {
        fun formatRemaining(seconds: Long?): String? = seconds?.let {
            val d = Duration.ofSeconds(it)
            val h = d.toHours()
            val m = d.toMinutes() % 60
            if (h > 0) "${h}h${m}m" else "${m}m"
        }
    }
}

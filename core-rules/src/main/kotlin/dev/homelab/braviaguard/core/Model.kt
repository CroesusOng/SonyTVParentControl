package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDate
import java.time.LocalDateTime
import java.time.LocalTime

/** How the package list is interpreted. */
enum class ListMode {
    /** List ignored; only budgets and curfews apply. */
    OFF,

    /** Only packages in [Policy.allowlist] may run. */
    ALLOWLIST,

    /** Everything may run except packages in [Policy.blocklist]. */
    BLOCKLIST,
}

/** Which packages a curfew or a grant applies to. */
sealed interface AppScope {
    fun covers(packageName: String): Boolean

    data object AllApps : AppScope {
        override fun covers(packageName: String) = true
    }

    data class Packages(val packages: Set<String>) : AppScope {
        override fun covers(packageName: String) = packageName in packages
    }

    companion object {
        fun of(packages: Collection<String>?): AppScope =
            if (packages.isNullOrEmpty()) AllApps else Packages(packages.toSet())
    }
}

/**
 * A recurring blocked time window.
 *
 * [days] are the days the window *opens*, not every day it covers. A curfew of
 * `start=21:30, end=06:30, days={FRIDAY}` runs from Friday 21:30 to Saturday 06:30 —
 * Saturday evening is not covered unless SATURDAY is also listed.
 *
 * `start == end` means a full 24 hours from `start`.
 */
data class Curfew(
    val id: String,
    val label: String,
    val days: Set<DayOfWeek>,
    val start: LocalTime,
    val end: LocalTime,
    val scope: AppScope = AppScope.AllApps,
) {
    /** True when the window runs past local midnight into the following day. */
    val crossesMidnight: Boolean get() = !end.isAfter(start)

    fun isActiveAt(at: LocalDateTime): Boolean {
        if (days.isEmpty()) return false
        val time = at.toLocalTime()
        return if (crossesMidnight) {
            (at.dayOfWeek in days && time >= start) ||
                (at.minusDays(1).dayOfWeek in days && time < end)
        } else {
            at.dayOfWeek in days && time >= start && time < end
        }
    }

    /** When the window currently covering [at] closes, or null when it is not active. */
    fun activeWindowEnd(at: LocalDateTime): LocalDateTime? {
        if (!isActiveAt(at)) return null
        if (!crossesMidnight) return at.toLocalDate().atTime(end)
        val openedToday = at.dayOfWeek in days && at.toLocalTime() >= start
        return if (openedToday) {
            at.toLocalDate().plusDays(1).atTime(end)
        } else {
            at.toLocalDate().atTime(end)
        }
    }

    /** The first time this curfew opens strictly after [after]. */
    fun nextStart(after: LocalDateTime): LocalDateTime? {
        if (days.isEmpty()) return null
        var day = after.toLocalDate()
        // 8 iterations so a window later today is preferred over the same weekday next week.
        repeat(8) {
            if (day.dayOfWeek in days) {
                val candidate = day.atTime(start)
                if (candidate.isAfter(after)) return candidate
            }
            day = day.plusDays(1)
        }
        return null
    }
}

/**
 * Time allowances. A missing entry means "no limit" — never "zero minutes".
 * Use a curfew to express a hard "not at all today".
 */
data class Budgets(
    val dailyMinutesByDay: Map<DayOfWeek, Int> = emptyMap(),
    val weeklyMinutes: Int? = null,
    val perAppDailyMinutes: Map<String, Int> = emptyMap(),
) {
    fun dailyLimitFor(day: DayOfWeek): Int? = dailyMinutesByDay[day]

    companion object {
        val NONE = Budgets()
    }
}

/** What happens when the backend on the Raspberry Pi cannot be reached. */
enum class OfflineBehavior {
    /** Keep applying the cached policy for as long as the backend is away. */
    KEEP_ENFORCING,

    /** Stop enforcing once the backend has been unreachable for [Policy.offlineGraceMinutes]. */
    OPEN_AFTER_GRACE,
}

data class Policy(
    val version: Long = 0,
    val enforcementEnabled: Boolean = false,
    val listMode: ListMode = ListMode.OFF,
    val allowlist: Set<String> = emptySet(),
    val blocklist: Set<String> = emptySet(),
    /** Never blocked, and never counted against a budget. */
    val exemptPackages: Set<String> = emptySet(),
    val budgets: Budgets = Budgets.NONE,
    val curfews: List<Curfew> = emptyList(),
    val offlineBehavior: OfflineBehavior = OfflineBehavior.KEEP_ENFORCING,
    val offlineGraceMinutes: Int = 60,
) {
    companion object {
        /**
         * What a freshly sideloaded app uses until the parent has paired it.
         * Deliberately inert: an unconfigured install must never lock anyone out of the TV.
         */
        val UNCONFIGURED = Policy()

        /**
         * System surfaces that must stay reachable or the TV becomes unusable — including
         * the launcher, because kicking a blocked app back to HOME has to land somewhere.
         * The app adds its own package and the resolved launcher to this set at runtime.
         */
        val SYSTEM_EXEMPT_HINTS: Set<String> = setOf(
            "com.android.systemui",
            "com.android.tv.settings",
            "com.android.settings",
            "com.android.packageinstaller",
            "com.google.android.packageinstaller",
            "com.android.permissioncontroller",
            "com.google.android.permissioncontroller",
            "com.google.android.tvlauncher",
            "com.google.android.apps.tv.launcherx",
            "com.sony.dtv.sonysystemsettings",
            "com.sony.dtv.sonysetupwizard",
            "com.sony.dtv.bravialifehackservice",
        )
    }
}

enum class GrantSource {
    /** Issued from the parent PWA over the LAN. */
    PARENT_APP,

    /** Redeemed on the TV from an offline rotating code, backend unreachable. */
    OFFLINE_CODE,
}

/** A temporary permission to run, issued by a parent. Overrides whatever it is told to. */
data class Grant(
    val id: String,
    val source: GrantSource,
    val issuedAt: Instant,
    val expiresAt: Instant,
    val scope: AppScope = AppScope.AllApps,
    val bypassList: Boolean = true,
    val bypassCurfew: Boolean = true,
    val bypassBudget: Boolean = true,
    val note: String? = null,
) {
    fun isActiveAt(now: Instant): Boolean = now.isBefore(expiresAt) && !now.isBefore(issuedAt)
}

/** Foreground time already spent, as the engine sees it. Seconds, so short sessions still count. */
data class UsageSnapshot(
    val day: LocalDate,
    val todayPerAppSeconds: Map<String, Long> = emptyMap(),
    val weekPerAppSeconds: Map<String, Long> = emptyMap(),
) {
    fun todayCountableSeconds(exempt: Set<String>): Long =
        todayPerAppSeconds.entries.sumOf { if (it.key in exempt) 0L else it.value }

    fun weekCountableSeconds(exempt: Set<String>): Long =
        weekPerAppSeconds.entries.sumOf { if (it.key in exempt) 0L else it.value }

    fun todaySecondsFor(packageName: String): Long = todayPerAppSeconds[packageName] ?: 0L
}

/** Headroom left under each limit that applies. null means that limit is not set. */
data class Remaining(
    val appSeconds: Long? = null,
    val dailySeconds: Long? = null,
    val weeklySeconds: Long? = null,
) {
    val tightestSeconds: Long?
        get() = listOfNotNull(appSeconds, dailySeconds, weeklySeconds).minOrNull()

    companion object {
        val UNLIMITED = Remaining()
    }
}

enum class AllowReason { EXEMPT, ENFORCEMENT_OFF, OFFLINE_GRACE, GRANT, WITHIN_LIMITS }

enum class BlockReason {
    NOT_IN_ALLOWLIST,
    IN_BLOCKLIST,
    CURFEW,
    APP_BUDGET_EXHAUSTED,
    DAILY_BUDGET_EXHAUSTED,
    WEEKLY_BUDGET_EXHAUSTED,
}

sealed interface Decision {
    val allowed: Boolean

    data class Allow(
        val reason: AllowReason,
        val remaining: Remaining = Remaining.UNLIMITED,
        val grant: Grant? = null,
    ) : Decision {
        override val allowed = true
    }

    /**
     * Carries structured facts, not a message: the TV layer owns wording and localisation.
     * [retryAt] is when this particular reason stops applying, where that is knowable.
     */
    data class Block(
        val reason: BlockReason,
        val curfew: Curfew? = null,
        val limitMinutes: Int? = null,
        val usedSeconds: Long? = null,
        val retryAt: Instant? = null,
    ) : Decision {
        override val allowed = false
    }
}

package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDateTime
import java.time.LocalTime
import java.time.ZoneId

/**
 * Asia/Shanghai is used throughout: fixed +08:00 with no DST, so every expected instant
 * in these tests is arithmetic a reader can check by hand.
 *
 * Reference week: Mon 2026-11-02 .. Sun 2026-11-08.
 */
val ZONE: ZoneId = ZoneId.of("Asia/Shanghai")

fun at(local: String): Instant = LocalDateTime.parse(local).atZone(ZONE).toInstant()

fun localAt(local: String): LocalDateTime = LocalDateTime.parse(local)

fun engine(weekStartsOn: DayOfWeek = DayOfWeek.MONDAY) = RuleEngine(ZONE, weekStartsOn)

fun ledger(weekStartsOn: DayOfWeek = DayOfWeek.MONDAY) = UsageLedger(ZONE, weekStartsOn)

const val NETFLIX = "com.netflix.ninja"
const val YOUTUBE = "com.google.android.youtube.tv"
const val KHAN = "org.khanacademy.android"
const val GUARD = "dev.homelab.braviaguard"
const val LAUNCHER = "com.google.android.tvlauncher"

fun curfew(
    id: String = "bedtime",
    label: String = "bedtime",
    days: Set<DayOfWeek>,
    start: String,
    end: String,
    packages: Set<String>? = null,
) = Curfew(
    id = id,
    label = label,
    days = days,
    start = LocalTime.parse(start),
    end = LocalTime.parse(end),
    scope = AppScope.of(packages),
)

fun grant(
    id: String = "g1",
    from: String,
    to: String,
    packages: Set<String>? = null,
    bypassList: Boolean = true,
    bypassCurfew: Boolean = true,
    bypassBudget: Boolean = true,
    source: GrantSource = GrantSource.PARENT_APP,
) = Grant(
    id = id,
    source = source,
    issuedAt = at(from),
    expiresAt = at(to),
    scope = AppScope.of(packages),
    bypassList = bypassList,
    bypassCurfew = bypassCurfew,
    bypassBudget = bypassBudget,
)

/** Usage expressed in minutes for readability; the engine works in seconds. */
fun usage(
    day: String,
    today: Map<String, Int> = emptyMap(),
    week: Map<String, Int> = today,
) = UsageSnapshot(
    day = java.time.LocalDate.parse(day),
    todayPerAppSeconds = today.mapValues { it.value * 60L },
    weekPerAppSeconds = week.mapValues { it.value * 60L },
)

val ENFORCING = Policy(
    version = 1,
    enforcementEnabled = true,
    exemptPackages = setOf(GUARD, LAUNCHER),
)

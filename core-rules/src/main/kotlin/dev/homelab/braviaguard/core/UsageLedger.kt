package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId

/** One uninterrupted stretch of a package being the foreground app. */
data class ForegroundSession(
    val packageName: String,
    val start: Instant,
    val end: Instant,
) {
    init {
        require(!end.isBefore(start)) { "session end precedes start: $packageName" }
    }
}

/** Per-local-day foreground seconds, keyed by package. */
typealias UsageStore = Map<LocalDate, Map<String, Long>>

/**
 * Turns raw foreground sessions into the per-day totals the rule engine consumes.
 *
 * The only interesting part is that a session spanning local midnight has to be split,
 * or a show watched from 23:40 to 00:20 would charge 40 minutes to the wrong day and let
 * a daily budget be dodged by starting late.
 */
class UsageLedger(
    private val zone: ZoneId,
    private val weekStartsOn: DayOfWeek = DayOfWeek.MONDAY,
) {

    /** Splits [session] at local midnight and returns the seconds owed to each day. */
    fun attribute(session: ForegroundSession): Map<LocalDate, Long> {
        val result = linkedMapOf<LocalDate, Long>()
        var cursor = session.start
        while (cursor.isBefore(session.end)) {
            val day = cursor.atZone(zone).toLocalDate()
            val dayEnd = day.plusDays(1).atStartOfDay(zone).toInstant()
            val sliceEnd = if (dayEnd.isBefore(session.end)) dayEnd else session.end
            val seconds = sliceEnd.epochSecond - cursor.epochSecond
            if (seconds > 0) {
                result[day] = (result[day] ?: 0L) + seconds
            }
            cursor = sliceEnd
        }
        return result
    }

    fun record(store: UsageStore, session: ForegroundSession): UsageStore {
        val mutable = store.mapValuesTo(linkedMapOf()) { it.value.toMutableMap() }
        attribute(session).forEach { (day, seconds) ->
            val perApp = mutable.getOrPut(day) { linkedMapOf() }
            perApp[session.packageName] = (perApp[session.packageName] ?: 0L) + seconds
        }
        return mutable.mapValues { it.value.toMap() }
    }

    fun recordAll(store: UsageStore, sessions: Iterable<ForegroundSession>): UsageStore =
        sessions.fold(store) { acc, session -> record(acc, session) }

    fun snapshot(store: UsageStore, now: Instant): UsageSnapshot {
        val today = now.atZone(zone).toLocalDate()
        val weekStart = startOfWeek(today)
        val week = linkedMapOf<String, Long>()
        store.forEach { (day, perApp) ->
            if (!day.isBefore(weekStart) && !day.isAfter(today)) {
                perApp.forEach { (pkg, seconds) -> week[pkg] = (week[pkg] ?: 0L) + seconds }
            }
        }
        return UsageSnapshot(
            day = today,
            todayPerAppSeconds = store[today].orEmpty(),
            weekPerAppSeconds = week,
        )
    }

    /** Drops days older than [keepDays] so the on-device store stays a few kilobytes. */
    fun prune(store: UsageStore, now: Instant, keepDays: Long = 90): UsageStore {
        val cutoff = now.atZone(zone).toLocalDate().minusDays(keepDays)
        return store.filterKeys { !it.isBefore(cutoff) }
    }

    fun startOfWeek(day: LocalDate): LocalDate {
        var date = day
        while (date.dayOfWeek != weekStartsOn) {
            date = date.minusDays(1)
        }
        return date
    }
}

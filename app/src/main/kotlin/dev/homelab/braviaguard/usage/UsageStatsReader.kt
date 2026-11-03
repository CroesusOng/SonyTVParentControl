package dev.homelab.braviaguard.usage

import android.app.usage.UsageEvents
import android.app.usage.UsageStatsManager
import android.content.Context
import android.util.Log
import dev.homelab.braviaguard.core.ForegroundSession
import dev.homelab.braviaguard.core.UsageLedger
import dev.homelab.braviaguard.core.UsageStore
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId

/**
 * Reconstructs foreground sessions from the system's own usage event log.
 *
 * Two jobs: filling in the stretches when the accessibility service was off (TV rebooted,
 * service toggled off, app updated), and giving the parent a report that still means
 * something if enforcement was only switched on yesterday.
 *
 * It reconstructs *sessions* rather than reading UsageStats totals so the result goes
 * through the same [UsageLedger] as live tracking and is split at local midnight the
 * same way — two sources of truth that disagree about day boundaries would be worse
 * than one imperfect source.
 */
class UsageStatsReader(context: Context, private val zone: ZoneId) {

    private val manager =
        context.getSystemService(Context.USAGE_STATS_SERVICE) as UsageStatsManager
    private val ledger = UsageLedger(zone)

    fun read(from: LocalDate, to: LocalDate): UsageStore {
        val start = from.atStartOfDay(zone).toInstant()
        val end = to.plusDays(1).atStartOfDay(zone).toInstant()
        return try {
            ledger.recordAll(emptyMap(), sessions(start, end))
        } catch (e: SecurityException) {
            Log.w(TAG, "usage access not granted", e)
            emptyMap()
        }
    }

    @Suppress("DEPRECATION") // MOVE_TO_* and ACTIVITY_* share numeric values; these work on 26+.
    private fun sessions(start: Instant, end: Instant): List<ForegroundSession> {
        val events = manager.queryEvents(start.toEpochMilli(), end.toEpochMilli())
        val openedAt = HashMap<String, Long>()
        val sessions = mutableListOf<ForegroundSession>()
        val event = UsageEvents.Event()

        while (events.hasNextEvent()) {
            events.getNextEvent(event)
            val pkg = event.packageName ?: continue
            when (event.eventType) {
                UsageEvents.Event.MOVE_TO_FOREGROUND -> openedAt[pkg] = event.timeStamp
                UsageEvents.Event.MOVE_TO_BACKGROUND -> {
                    val opened = openedAt.remove(pkg) ?: continue
                    if (event.timeStamp > opened) {
                        sessions += ForegroundSession(
                            pkg,
                            Instant.ofEpochMilli(opened),
                            Instant.ofEpochMilli(event.timeStamp),
                        )
                    }
                }
            }
        }

        // Anything still open at the end of the window ran until the window closed.
        openedAt.forEach { (pkg, opened) ->
            if (end.toEpochMilli() > opened) {
                sessions += ForegroundSession(pkg, Instant.ofEpochMilli(opened), end)
            }
        }
        return sessions
    }

    private companion object {
        const val TAG = "UsageStatsReader"
    }
}

package dev.homelab.braviaguard.core

import java.time.LocalDate
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class UsageLedgerTest {

    private val ledger = ledger()

    private fun session(pkg: String, from: String, to: String) =
        ForegroundSession(pkg, at(from), at(to))

    @Test
    fun `a session inside one day is charged to that day`() {
        val attributed = ledger.attribute(session(NETFLIX, "2026-11-03T10:00", "2026-11-03T10:30"))
        assertEquals(mapOf(LocalDate.parse("2026-11-03") to 1800L), attributed)
    }

    @Test
    fun `a session across local midnight is split, so a late start cannot dodge a daily budget`() {
        val attributed = ledger.attribute(session(NETFLIX, "2026-11-03T23:40", "2026-11-04T00:20"))
        assertEquals(
            mapOf(
                LocalDate.parse("2026-11-03") to 1200L,
                LocalDate.parse("2026-11-04") to 1200L,
            ),
            attributed,
        )
    }

    @Test
    fun `a TV left on for days is split across every day it covers`() {
        val attributed = ledger.attribute(session(NETFLIX, "2026-11-02T22:00", "2026-11-04T02:00"))
        assertEquals(
            mapOf(
                LocalDate.parse("2026-11-02") to 2 * 3600L,
                LocalDate.parse("2026-11-03") to 24 * 3600L,
                LocalDate.parse("2026-11-04") to 2 * 3600L,
            ),
            attributed,
        )
    }

    @Test
    fun `a zero length session contributes nothing`() {
        assertTrue(ledger.attribute(session(NETFLIX, "2026-11-03T10:00", "2026-11-03T10:00")).isEmpty())
    }

    @Test
    fun `repeated sessions for one app accumulate`() {
        val store = ledger.recordAll(
            emptyMap(),
            listOf(
                session(NETFLIX, "2026-11-03T10:00", "2026-11-03T10:30"),
                session(NETFLIX, "2026-11-03T14:00", "2026-11-03T14:10"),
                session(KHAN, "2026-11-03T15:00", "2026-11-03T15:05"),
            ),
        )
        val day = store[LocalDate.parse("2026-11-03")]!!
        assertEquals(2400L, day[NETFLIX])
        assertEquals(300L, day[KHAN])
    }

    @Test
    fun `snapshot separates today from the running week`() {
        val store = ledger.recordAll(
            emptyMap(),
            listOf(
                session(NETFLIX, "2026-11-02T20:00", "2026-11-02T21:00"), // Monday
                session(NETFLIX, "2026-11-03T20:00", "2026-11-03T20:30"), // Tuesday
                session(KHAN, "2026-11-03T21:00", "2026-11-03T21:15"),    // Tuesday
            ),
        )
        val snapshot = ledger.snapshot(store, at("2026-11-03T23:00"))

        assertEquals(LocalDate.parse("2026-11-03"), snapshot.day)
        assertEquals(1800L, snapshot.todayPerAppSeconds[NETFLIX])
        assertEquals(900L, snapshot.todayPerAppSeconds[KHAN])
        assertEquals(3600L + 1800L, snapshot.weekPerAppSeconds[NETFLIX])
        assertEquals(900L, snapshot.weekPerAppSeconds[KHAN])
    }

    @Test
    fun `the week resets on Monday`() {
        val store = ledger.recordAll(
            emptyMap(),
            listOf(
                session(NETFLIX, "2026-11-01T20:00", "2026-11-01T21:00"), // previous Sunday
                session(NETFLIX, "2026-11-02T20:00", "2026-11-02T20:30"), // Monday
            ),
        )
        val snapshot = ledger.snapshot(store, at("2026-11-02T23:00"))
        // Sunday belonged to the previous week, so only Monday's 30 minutes count.
        assertEquals(1800L, snapshot.weekPerAppSeconds[NETFLIX])
    }

    @Test
    fun `a Sunday snapshot covers the whole Monday to Sunday week`() {
        val store = ledger.recordAll(
            emptyMap(),
            (2..8).map { day ->
                session(NETFLIX, "2026-11-%02dT20:00".format(day), "2026-11-%02dT20:10".format(day))
            },
        )
        val snapshot = ledger.snapshot(store, at("2026-11-08T23:00"))
        assertEquals(7 * 600L, snapshot.weekPerAppSeconds[NETFLIX])
    }

    @Test
    fun `startOfWeek walks back to Monday`() {
        assertEquals(LocalDate.parse("2026-11-02"), ledger.startOfWeek(LocalDate.parse("2026-11-08")))
        assertEquals(LocalDate.parse("2026-11-02"), ledger.startOfWeek(LocalDate.parse("2026-11-02")))
    }

    @Test
    fun `prune keeps the retention window and drops what is older`() {
        val store = mapOf(
            LocalDate.parse("2026-08-01") to mapOf(NETFLIX to 60L),
            LocalDate.parse("2026-08-05") to mapOf(NETFLIX to 60L),
            LocalDate.parse("2026-11-03") to mapOf(NETFLIX to 60L),
        )
        val pruned = ledger.prune(store, at("2026-11-03T12:00"), keepDays = 90)
        assertEquals(
            setOf(LocalDate.parse("2026-08-05"), LocalDate.parse("2026-11-03")),
            pruned.keys,
        )
    }

    @Test
    fun `snapshot feeds straight into the rule engine`() {
        val policy = ENFORCING.copy(
            budgets = Budgets(dailyMinutesByDay = mapOf(java.time.DayOfWeek.TUESDAY to 45)),
        )
        val store = ledger.record(
            emptyMap(),
            session(NETFLIX, "2026-11-03T19:00", "2026-11-03T19:50"),
        )
        val snapshot = ledger.snapshot(store, at("2026-11-03T20:00"))
        val decision = engine().evaluate(NETFLIX, policy, snapshot, now = at("2026-11-03T20:00"))
        assertEquals(BlockReason.DAILY_BUDGET_EXHAUSTED, (decision as Decision.Block).reason)
    }
}

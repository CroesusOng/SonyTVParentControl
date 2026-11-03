package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class CurfewTest {

    private val homework = curfew(
        id = "homework",
        days = setOf(DayOfWeek.TUESDAY),
        start = "18:00",
        end = "20:00",
    )

    @Test
    fun `window includes its start and excludes its end`() {
        assertTrue(homework.isActiveAt(localAt("2026-11-03T18:00")))
        assertTrue(homework.isActiveAt(localAt("2026-11-03T19:59:59")))
        assertFalse(homework.isActiveAt(localAt("2026-11-03T20:00")))
        assertFalse(homework.isActiveAt(localAt("2026-11-03T17:59:59")))
    }

    @Test
    fun `window only applies on its listed weekdays`() {
        // 2026-11-04 is a Wednesday, same clock time.
        assertFalse(homework.isActiveAt(localAt("2026-11-04T19:00")))
    }

    @Test
    fun `empty day set is never active`() {
        val never = curfew(days = emptySet(), start = "00:00", end = "23:59")
        assertFalse(never.isActiveAt(localAt("2026-11-03T12:00")))
        assertNull(never.nextStart(localAt("2026-11-03T12:00")))
    }

    // --- the interesting case: a window that runs past midnight ---

    private val bedtime = curfew(
        id = "bedtime",
        days = setOf(DayOfWeek.FRIDAY),
        start = "21:30",
        end = "06:30",
    )

    @Test
    fun `midnight crossing window is flagged as such`() {
        assertTrue(bedtime.crossesMidnight)
        assertFalse(homework.crossesMidnight)
    }

    @Test
    fun `midnight crossing window covers both sides of midnight`() {
        assertTrue(bedtime.isActiveAt(localAt("2026-11-06T21:30")))  // Fri, opens
        assertTrue(bedtime.isActiveAt(localAt("2026-11-06T23:59")))  // Fri, late
        assertTrue(bedtime.isActiveAt(localAt("2026-11-07T00:00")))  // Sat, past midnight
        assertTrue(bedtime.isActiveAt(localAt("2026-11-07T06:29")))  // Sat, nearly over
        assertFalse(bedtime.isActiveAt(localAt("2026-11-07T06:30"))) // Sat, closed
        assertFalse(bedtime.isActiveAt(localAt("2026-11-06T21:29"))) // Fri, not yet
    }

    @Test
    fun `days name the day the window opens, not every day it touches`() {
        // Saturday is not listed, so Saturday night is free even though Saturday
        // morning was covered by Friday's window.
        assertFalse(bedtime.isActiveAt(localAt("2026-11-07T22:00")))
        // And Friday morning is free, because Thursday is not listed.
        assertFalse(bedtime.isActiveAt(localAt("2026-11-06T06:00")))
    }

    @Test
    fun `activeWindowEnd resolves to the correct calendar day`() {
        assertEquals(localAt("2026-11-07T06:30"), bedtime.activeWindowEnd(localAt("2026-11-06T22:00")))
        assertEquals(localAt("2026-11-07T06:30"), bedtime.activeWindowEnd(localAt("2026-11-07T03:00")))
        assertEquals(localAt("2026-11-03T20:00"), homework.activeWindowEnd(localAt("2026-11-03T18:30")))
        assertNull(homework.activeWindowEnd(localAt("2026-11-03T21:00")))
    }

    @Test
    fun `equal start and end means a full 24 hours`() {
        val allSaturday = curfew(days = setOf(DayOfWeek.SATURDAY), start = "09:00", end = "09:00")
        assertTrue(allSaturday.isActiveAt(localAt("2026-11-07T09:00")))
        assertTrue(allSaturday.isActiveAt(localAt("2026-11-07T23:59")))
        assertTrue(allSaturday.isActiveAt(localAt("2026-11-08T08:59")))
        assertFalse(allSaturday.isActiveAt(localAt("2026-11-08T09:00")))
        assertFalse(allSaturday.isActiveAt(localAt("2026-11-07T08:59")))
    }

    @Test
    fun `nextStart prefers a window later today over next week`() {
        assertEquals(localAt("2026-11-03T18:00"), homework.nextStart(localAt("2026-11-03T12:00")))
    }

    @Test
    fun `nextStart is strictly after the given instant`() {
        assertEquals(localAt("2026-11-10T18:00"), homework.nextStart(localAt("2026-11-03T18:00")))
        assertEquals(localAt("2026-11-10T18:00"), homework.nextStart(localAt("2026-11-03T19:00")))
    }

    @Test
    fun `scoped curfew only covers its own packages`() {
        val gamesOnly = curfew(
            days = setOf(DayOfWeek.TUESDAY),
            start = "18:00",
            end = "20:00",
            packages = setOf(NETFLIX),
        )
        assertTrue(gamesOnly.scope.covers(NETFLIX))
        assertFalse(gamesOnly.scope.covers(KHAN))
    }
}

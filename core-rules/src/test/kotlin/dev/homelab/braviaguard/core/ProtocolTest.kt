package dev.homelab.braviaguard.core

import java.time.DayOfWeek
import java.time.LocalDate
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class ProtocolTest {

    private val policy = Policy(
        version = 42,
        enforcementEnabled = true,
        listMode = ListMode.BLOCKLIST,
        allowlist = setOf(KHAN),
        blocklist = setOf(NETFLIX, YOUTUBE),
        exemptPackages = setOf(GUARD, LAUNCHER),
        budgets = Budgets(
            dailyMinutesByDay = mapOf(DayOfWeek.MONDAY to 60, DayOfWeek.SATURDAY to 180),
            weeklyMinutes = 600,
            perAppDailyMinutes = mapOf(NETFLIX to 45),
        ),
        curfews = listOf(
            curfew(id = "bedtime", days = setOf(DayOfWeek.FRIDAY), start = "21:30", end = "06:30"),
            curfew(
                id = "homework",
                days = setOf(DayOfWeek.MONDAY, DayOfWeek.TUESDAY),
                start = "18:00",
                end = "20:00",
                packages = setOf(NETFLIX),
            ),
        ),
        offlineBehavior = OfflineBehavior.OPEN_AFTER_GRACE,
        offlineGraceMinutes = 30,
    )

    @Test
    fun `policy survives a round trip through JSON`() {
        val json = ProtocolJson.encodeToString(PolicyDto.serializer(), policy.toDto())
        val back = ProtocolJson.decodeFromString(PolicyDto.serializer(), json).toDomain()
        assertEquals(policy, back)
    }

    @Test
    fun `times are written in the HH mm form the Go side and jq expect`() {
        val json = ProtocolJson.encodeToString(PolicyDto.serializer(), policy.toDto())
        assertTrue(json.contains("\"start\":\"21:30\""), json)
        assertTrue(json.contains("\"end\":\"06:30\""), json)
        assertTrue(json.contains("\"MONDAY\":60"), json)
    }

    @Test
    fun `a curfew covering every app serialises packages as absent, not as an empty list`() {
        val dto = policy.toDto()
        assertNull(dto.curfews.first { it.id == "bedtime" }.packages)
        assertEquals(listOf(NETFLIX), dto.curfews.first { it.id == "homework" }.packages)
    }

    @Test
    fun `an empty object decodes to an inert policy`() {
        val decoded = ProtocolJson.decodeFromString(PolicyDto.serializer(), "{}").toDomain()
        assertEquals(Policy.UNCONFIGURED, decoded)
    }

    @Test
    fun `fields a newer backend adds are ignored instead of crashing the TV`() {
        val json = """{"version":7,"enforcementEnabled":true,"somethingNewIn2027":{"a":1}}"""
        val decoded = ProtocolJson.decodeFromString(PolicyDto.serializer(), json).toDomain()
        assertEquals(7L, decoded.version)
        assertTrue(decoded.enforcementEnabled)
    }

    @Test
    fun `an unknown list mode degrades to OFF rather than throwing`() {
        val json = """{"listMode":"SOMETHING_ELSE"}"""
        assertEquals(
            ListMode.OFF,
            ProtocolJson.decodeFromString(PolicyDto.serializer(), json).toDomain().listMode,
        )
    }

    @Test
    fun `grant survives a round trip, including the all-apps scope`() {
        val global = grant(from = "2026-11-03T19:00", to = "2026-11-03T20:00")
        assertEquals(global, global.toDto().toDomain())
        assertNull(global.toDto().packages)

        val scoped = grant(
            id = "g2",
            from = "2026-11-03T19:00",
            to = "2026-11-03T20:00",
            packages = setOf(YOUTUBE, NETFLIX),
            bypassCurfew = false,
        )
        assertEquals(scoped, scoped.toDto().toDomain())
    }

    @Test
    fun `usage store survives a round trip`() {
        val store: UsageStore = mapOf(
            LocalDate.parse("2026-11-02") to mapOf(NETFLIX to 3600L),
            LocalDate.parse("2026-11-03") to mapOf(NETFLIX to 1800L, KHAN to 900L),
        )
        assertEquals(store, store.toDtos().toUsageStore())
        assertEquals(listOf("2026-11-02", "2026-11-03"), store.toDtos().map { it.day })
    }

    @Test
    fun `sync response parses with only the fields the backend had to send`() {
        val json = """{"serverTimeMillis":1762000000000}"""
        val response = ProtocolJson.decodeFromString(SyncResponse.serializer(), json)
        assertEquals(1762000000000L, response.serverTimeMillis)
        assertNull(response.policy)
        assertTrue(response.grants.isEmpty())
        // A missing revision has to read as zero rather than throw: that is what an older
        // backend sends, and zero is exactly the "I know nothing" the long poll wants.
        assertEquals(0L, response.revision)
    }

    @Test
    fun `sync response carries the revision the long poll sends back`() {
        val json = """{"serverTimeMillis":1762000000000,"revision":47,"grants":[]}"""
        val response = ProtocolJson.decodeFromString(SyncResponse.serializer(), json)
        assertEquals(47L, response.revision)
    }
}

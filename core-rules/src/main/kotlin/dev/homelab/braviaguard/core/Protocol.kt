package dev.homelab.braviaguard.core

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDate
import java.time.LocalTime

/**
 * The wire format shared with the Go backend on the Raspberry Pi.
 *
 * Deliberately primitives only — epoch millis, "HH:mm", weekday names — so the Go side
 * needs no date library agreement with java.time, and so a human can read and hand-edit
 * the JSON files on the Pi with `jq`.
 */
val ProtocolJson: Json = Json {
    ignoreUnknownKeys = true
    encodeDefaults = true
    explicitNulls = false
}

@Serializable
data class CurfewDto(
    val id: String,
    val label: String = "",
    val days: List<String> = emptyList(),
    val start: String,
    val end: String,
    /** null or empty means every app. */
    val packages: List<String>? = null,
)

@Serializable
data class PolicyDto(
    val version: Long = 0,
    val enforcementEnabled: Boolean = false,
    val listMode: String = "OFF",
    val allowlist: List<String> = emptyList(),
    val blocklist: List<String> = emptyList(),
    val exemptPackages: List<String> = emptyList(),
    /** Weekday name -> minutes. A missing weekday means no daily limit that day. */
    val dailyMinutes: Map<String, Int> = emptyMap(),
    val weeklyMinutes: Int? = null,
    val perAppDailyMinutes: Map<String, Int> = emptyMap(),
    val curfews: List<CurfewDto> = emptyList(),
    val offlineBehavior: String = "KEEP_ENFORCING",
    val offlineGraceMinutes: Int = 60,
)

@Serializable
data class GrantDto(
    val id: String,
    val source: String = "PARENT_APP",
    val issuedAtMillis: Long,
    val expiresAtMillis: Long,
    val packages: List<String>? = null,
    val bypassList: Boolean = true,
    val bypassCurfew: Boolean = true,
    val bypassBudget: Boolean = true,
    val note: String? = null,
)

@Serializable
data class SyncResponse(
    val serverTimeMillis: Long,
    /** Omitted when the device already has this version. */
    val policy: PolicyDto? = null,
    val grants: List<GrantDto> = emptyList(),
    /**
     * Echoed back on the next request so the backend can hold that request open until
     * something actually changes. Defaults to zero, which reads as "I know nothing yet"
     * and gets an immediate reply.
     */
    val revision: Long = 0,
    val escapeSecretBase32: String? = null,
)

@Serializable
data class DayUsageDto(
    /** ISO local date, e.g. "2026-11-03". */
    val day: String,
    val perAppSeconds: Map<String, Long> = emptyMap(),
)

@Serializable
data class UsageReportRequest(
    val deviceId: String,
    val days: List<DayUsageDto> = emptyList(),
    /** Package name to human label, so the parent app never shows a bare package name. */
    val labels: Map<String, String> = emptyMap(),
)

@Serializable
data class EnrollRequest(
    val pairingCode: String,
    val deviceName: String,
    val model: String = "",
    val androidRelease: String = "",
)

@Serializable
data class EnrollResponse(
    val deviceId: String,
    val token: String,
    val escapeSecretBase32: String,
    val serverTimeMillis: Long,
)

@Serializable
data class BlockEventRequest(
    val deviceId: String,
    val packageName: String,
    val appLabel: String? = null,
    val reason: String,
    val atMillis: Long,
)

@Serializable
data class ApiError(val error: String, val detail: String? = null)

// ---------- mapping ----------

fun PolicyDto.toDomain(): Policy = Policy(
    version = version,
    enforcementEnabled = enforcementEnabled,
    listMode = runCatching { ListMode.valueOf(listMode) }.getOrDefault(ListMode.OFF),
    allowlist = allowlist.toSet(),
    blocklist = blocklist.toSet(),
    exemptPackages = exemptPackages.toSet(),
    budgets = Budgets(
        dailyMinutesByDay = dailyMinutes.mapNotNull { (name, minutes) ->
            runCatching { DayOfWeek.valueOf(name.uppercase()) }.getOrNull()?.let { it to minutes }
        }.toMap(),
        weeklyMinutes = weeklyMinutes,
        perAppDailyMinutes = perAppDailyMinutes,
    ),
    curfews = curfews.map { it.toDomain() },
    offlineBehavior = runCatching { OfflineBehavior.valueOf(offlineBehavior) }
        .getOrDefault(OfflineBehavior.KEEP_ENFORCING),
    offlineGraceMinutes = offlineGraceMinutes,
)

fun Policy.toDto(): PolicyDto = PolicyDto(
    version = version,
    enforcementEnabled = enforcementEnabled,
    listMode = listMode.name,
    allowlist = allowlist.sorted(),
    blocklist = blocklist.sorted(),
    exemptPackages = exemptPackages.sorted(),
    dailyMinutes = budgets.dailyMinutesByDay.entries
        .sortedBy { it.key.value }
        .associate { it.key.name to it.value },
    weeklyMinutes = budgets.weeklyMinutes,
    perAppDailyMinutes = budgets.perAppDailyMinutes,
    curfews = curfews.map { it.toDto() },
    offlineBehavior = offlineBehavior.name,
    offlineGraceMinutes = offlineGraceMinutes,
)

fun CurfewDto.toDomain(): Curfew = Curfew(
    id = id,
    label = label,
    days = days.mapNotNull { runCatching { DayOfWeek.valueOf(it.uppercase()) }.getOrNull() }.toSet(),
    start = LocalTime.parse(start),
    end = LocalTime.parse(end),
    scope = AppScope.of(packages),
)

fun Curfew.toDto(): CurfewDto = CurfewDto(
    id = id,
    label = label,
    days = days.sortedBy { it.value }.map { it.name },
    start = start.toString(),
    end = end.toString(),
    packages = (scope as? AppScope.Packages)?.packages?.sorted(),
)

fun GrantDto.toDomain(): Grant = Grant(
    id = id,
    source = runCatching { GrantSource.valueOf(source) }.getOrDefault(GrantSource.PARENT_APP),
    issuedAt = Instant.ofEpochMilli(issuedAtMillis),
    expiresAt = Instant.ofEpochMilli(expiresAtMillis),
    scope = AppScope.of(packages),
    bypassList = bypassList,
    bypassCurfew = bypassCurfew,
    bypassBudget = bypassBudget,
    note = note,
)

fun Grant.toDto(): GrantDto = GrantDto(
    id = id,
    source = source.name,
    issuedAtMillis = issuedAt.toEpochMilli(),
    expiresAtMillis = expiresAt.toEpochMilli(),
    packages = (scope as? AppScope.Packages)?.packages?.sorted(),
    bypassList = bypassList,
    bypassCurfew = bypassCurfew,
    bypassBudget = bypassBudget,
    note = note,
)

fun UsageStore.toDtos(): List<DayUsageDto> = entries
    .sortedBy { it.key }
    .map { (day, perApp) -> DayUsageDto(day.toString(), perApp) }

fun List<DayUsageDto>.toUsageStore(): UsageStore = associate { dto ->
    LocalDate.parse(dto.day) to dto.perAppSeconds
}

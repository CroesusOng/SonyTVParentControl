package dev.homelab.braviaguard.ui

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

private val CLOCK = DateTimeFormatter.ofPattern("HH:mm")

/** Compact and locale-neutral: readable at three metres in either language. */
fun formatDuration(seconds: Long): String {
    if (seconds < 60) return "<1m"
    val hours = seconds / 3600
    val minutes = (seconds % 3600) / 60
    return if (hours > 0) "${hours}h${minutes}m" else "${minutes}m"
}

fun formatAgo(sinceMillis: Long, nowMillis: Long = System.currentTimeMillis()): String =
    formatDuration(((nowMillis - sinceMillis) / 1000).coerceAtLeast(0))

fun formatClock(millis: Long, zone: ZoneId): String =
    Instant.ofEpochMilli(millis).atZone(zone).format(CLOCK)

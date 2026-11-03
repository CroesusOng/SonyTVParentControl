package dev.homelab.braviaguard.enforce

import android.content.Context
import dev.homelab.braviaguard.R
import dev.homelab.braviaguard.core.BlockReason
import dev.homelab.braviaguard.core.Decision
import java.time.Instant
import java.time.LocalDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/** What the overlay actually puts on screen. Built once, on the main thread. */
data class BlockPresentation(
    val appLabel: String,
    val reason: String,
    val retry: String?,
    val hint: String,
    val offline: Boolean,
)

/** Turns the engine's structured verdict into wording, so the engine stays locale-free. */
object BlockMessages {

    private val TIME = DateTimeFormatter.ofPattern("HH:mm")
    private val DATE_TIME = DateTimeFormatter.ofPattern("MM-dd HH:mm")

    fun present(
        context: Context,
        block: Decision.Block,
        appLabel: String,
        zone: ZoneId,
        now: Instant,
        offline: Boolean,
    ) = BlockPresentation(
        appLabel = appLabel,
        reason = reason(context, block, appLabel),
        retry = block.retryAt?.let { context.getString(R.string.block_retry_at, time(it, now, zone)) },
        hint = context.getString(
            if (offline) R.string.block_offline_hint else R.string.block_ask_parent,
        ),
        offline = offline,
    )

    private fun reason(context: Context, block: Decision.Block, appLabel: String): String =
        when (block.reason) {
            BlockReason.NOT_IN_ALLOWLIST ->
                context.getString(R.string.block_reason_not_allowed, appLabel)

            BlockReason.IN_BLOCKLIST ->
                context.getString(R.string.block_reason_blocked, appLabel)

            BlockReason.CURFEW -> {
                val label = block.curfew?.label?.takeIf { it.isNotBlank() }
                if (label != null) {
                    context.getString(R.string.block_reason_curfew, label)
                } else {
                    context.getString(R.string.block_reason_curfew_unnamed)
                }
            }

            BlockReason.APP_BUDGET_EXHAUSTED ->
                context.getString(
                    R.string.block_reason_app_budget, appLabel, block.limitMinutes ?: 0,
                )

            BlockReason.DAILY_BUDGET_EXHAUSTED ->
                context.getString(R.string.block_reason_daily_budget, block.limitMinutes ?: 0)

            BlockReason.WEEKLY_BUDGET_EXHAUSTED ->
                context.getString(R.string.block_reason_weekly_budget, block.limitMinutes ?: 0)
        }

    /** "06:30" when it is still today, "11-04 06:30" when it is not. */
    private fun time(target: Instant, now: Instant, zone: ZoneId): String {
        val targetLocal = LocalDateTime.ofInstant(target, zone)
        val nowLocal = LocalDateTime.ofInstant(now, zone)
        val formatter = if (targetLocal.toLocalDate() == nowLocal.toLocalDate()) TIME else DATE_TIME
        return targetLocal.format(formatter)
    }
}

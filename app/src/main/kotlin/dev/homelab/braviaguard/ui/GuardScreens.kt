package dev.homelab.braviaguard.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Button
import androidx.tv.material3.Text
import dev.homelab.braviaguard.GuardApp
import dev.homelab.braviaguard.R
import dev.homelab.braviaguard.enforce.GuardSnapshot
import dev.homelab.braviaguard.net.DiscoveredBackend
import dev.homelab.braviaguard.usage.SystemAccess
import kotlinx.coroutines.launch

/**
 * The TV side of the UI is deliberately read-only about rules. Nothing here can relax a
 * limit, which is why there is no PIN on this screen: there is nothing behind it worth
 * guarding. Pairing is the one state-changing action, and unpairing is gated on an
 * unlock code from the parent app.
 */
@Composable
fun GuardRoot(app: GuardApp, resumeTick: Int) {
    val snapshot by app.enforcement.snapshot.collectAsStateCompat()
    if (snapshot.paired) {
        DashboardScreen(app, snapshot, resumeTick)
    } else {
        PairingScreen(app, resumeTick)
    }
}

@Composable
private fun DashboardScreen(app: GuardApp, snapshot: GuardSnapshot, resumeTick: Int) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val firstAction = remember { FocusRequester() }
    var syncing by remember { mutableStateOf(false) }
    var unpairCode by remember { mutableStateOf("") }
    var unpairError by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(Unit) { runCatching { firstAction.requestFocus() } }

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        // 10-foot UI: the outer padding also keeps content clear of TV overscan.
        contentPadding = androidx.compose.foundation.layout.PaddingValues(
            horizontal = 56.dp,
            vertical = 40.dp,
        ),
        verticalArrangement = Arrangement.spacedBy(20.dp),
    ) {
        item { Header(snapshot) }

        item { PermissionWarnings(resumeTick) }

        item {
            Section(
                title = stringRes(R.string.dash_today),
                accent = if (snapshot.enforcing) BorderIdle else Warn,
            ) {
                StatLine(stringRes(R.string.dash_today), formatDuration(snapshot.todaySeconds))
                StatLine(stringRes(R.string.dash_week), formatDuration(snapshot.weekSeconds))
                StatLine(
                    label = stringRes(R.string.dash_remaining, "").trim(),
                    value = snapshot.remainingTodaySeconds
                        ?.let { formatDuration(it) }
                        ?: stringRes(R.string.dash_no_limit),
                    valueColor = when {
                        snapshot.remainingTodaySeconds == null -> TextMuted
                        snapshot.remainingTodaySeconds < 600 -> Warn
                        else -> Ok
                    },
                )
                snapshot.blockedLabel?.let {
                    Body(stringRes(R.string.dash_blocked_now, it), Danger)
                }
                snapshot.activeGrantExpiryMillis?.let {
                    Body(
                        stringRes(R.string.dash_grant_until, formatClock(it, app.zone)),
                        Ok,
                    )
                }
            }
        }

        item {
            Section(title = stringRes(R.string.dash_top_apps)) {
                if (snapshot.topAppsToday.isEmpty()) {
                    Body(stringRes(R.string.dash_no_usage), TextMuted)
                } else {
                    snapshot.topAppsToday.forEach { (label, seconds) ->
                        StatLine(label, formatDuration(seconds))
                    }
                }
            }
        }

        item {
            Section(
                title = stringRes(R.string.dash_title),
                accent = if (snapshot.unreachableSinceMillis > 0) Warn else BorderIdle,
            ) {
                Body(stringRes(R.string.dash_paired_with, snapshot.baseUrl.orEmpty()), TextMuted)
                Body(stringRes(R.string.dash_policy_version, snapshot.policyVersion), TextMuted)
                Body(stringRes(R.string.dash_curfews, snapshot.curfewCount), TextMuted)
                when {
                    snapshot.unreachableSinceMillis > 0 -> Body(
                        stringRes(
                            R.string.dash_offline_since,
                            formatAgo(snapshot.unreachableSinceMillis),
                        ),
                        Warn,
                    )

                    snapshot.lastSyncMillis > 0 -> Body(
                        stringRes(R.string.dash_last_sync, formatAgo(snapshot.lastSyncMillis)),
                        TextMuted,
                    )

                    else -> Body(stringRes(R.string.dash_never_synced), Warn)
                }
                Body(stringRes(R.string.dash_settings_note), TextMuted, size = 17)

                Spacer(Modifier.height(6.dp))
                Button(
                    onClick = {
                        syncing = true
                        scope.launch {
                            app.enforcement.syncOnce()
                            syncing = false
                        }
                    },
                    modifier = Modifier.focusRequester(firstAction),
                ) {
                    Text(
                        if (syncing) stringRes(R.string.dash_syncing)
                        else stringRes(R.string.dash_sync_now),
                        fontSize = 20.sp,
                    )
                }
            }
        }

        item {
            Section(title = stringRes(R.string.dash_unpair), accent = BorderIdle) {
                Body(stringRes(R.string.dash_unpair_prompt), TextMuted, size = 17)
                TvTextField(
                    value = unpairCode,
                    onValueChange = { unpairCode = it.filter(Char::isDigit).take(6) },
                    label = stringRes(R.string.block_code_label),
                    keyboardType = KeyboardType.NumberPassword,
                )
                unpairError?.let { Body(it, Danger, size = 17) }
                Button(
                    onClick = {
                        scope.launch {
                            val ok = app.enforcement.unpair(unpairCode)
                            unpairError = if (ok) null
                            else context.getString(R.string.dash_unpair_failed)
                            unpairCode = ""
                        }
                    },
                    enabled = unpairCode.length == 6,
                ) {
                    Text(stringRes(R.string.dash_unpair), fontSize = 20.sp)
                }
            }
        }
    }
}

@Composable
private fun PairingScreen(app: GuardApp, resumeTick: Int) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val firstField = remember { FocusRequester() }
    var address by remember { mutableStateOf("") }
    var code by remember { mutableStateOf("") }
    var working by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    var discovered by remember { mutableStateOf(emptyList<DiscoveredBackend>()) }
    LaunchedEffect(app) {
        app.discovery.discover().collect { discovered = it }
    }

    LaunchedEffect(Unit) { runCatching { firstField.requestFocus() } }

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = androidx.compose.foundation.layout.PaddingValues(
            horizontal = 56.dp,
            vertical = 40.dp,
        ),
        verticalArrangement = Arrangement.spacedBy(20.dp),
    ) {
        item {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(
                    stringRes(R.string.pair_title),
                    fontSize = 40.sp,
                    fontWeight = FontWeight.Bold,
                    color = TextPrimary,
                )
                Body(stringRes(R.string.pair_body), TextMuted)
            }
        }

        item { PermissionWarnings(resumeTick) }

        item {
            Section(title = stringRes(R.string.pair_found)) {
                if (discovered.isEmpty()) {
                    Body(stringRes(R.string.pair_discovering), TextMuted, size = 18)
                }
            }
        }

        items(discovered, key = { it.baseUrl }) { backend ->
            Button(onClick = { address = backend.baseUrl }, modifier = Modifier.fillMaxWidth()) {
                Text("${backend.name} — ${backend.baseUrl}", fontSize = 20.sp)
            }
        }

        item {
            Section(title = stringRes(R.string.pair_manual_label)) {
                TvTextField(
                    value = address,
                    onValueChange = { address = it },
                    label = stringRes(R.string.pair_manual_label),
                    placeholder = stringRes(R.string.pair_manual_hint),
                    keyboardType = KeyboardType.Uri,
                    modifier = Modifier.focusRequester(firstField),
                )
                TvTextField(
                    value = code,
                    onValueChange = { code = it.filter(Char::isDigit).take(6) },
                    label = stringRes(R.string.pair_code_label),
                    keyboardType = KeyboardType.NumberPassword,
                )
                error?.let { Body(it, Danger, size = 18) }
                Button(
                    onClick = {
                        if (address.isBlank() || code.length != 6) {
                            error = context.getString(R.string.pair_need_code)
                            return@Button
                        }
                        working = true
                        error = null
                        scope.launch {
                            app.enforcement.pair(address, code)
                                .onFailure {
                                    error = context.getString(
                                        R.string.pair_failed,
                                        it.message ?: it.javaClass.simpleName,
                                    )
                                }
                            working = false
                        }
                    },
                    enabled = !working,
                ) {
                    Text(
                        if (working) stringRes(R.string.pair_working) else stringRes(R.string.pair_submit),
                        fontSize = 20.sp,
                    )
                }
            }
        }
    }
}

@Composable
private fun Header(snapshot: GuardSnapshot) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(
            stringRes(R.string.dash_title),
            fontSize = 40.sp,
            fontWeight = FontWeight.Bold,
            color = TextPrimary,
        )
        Spacer(Modifier.width(20.dp))
        Text(
            text = if (snapshot.enforcing) {
                "● " + stringRes(R.string.dash_enforcing_on)
            } else {
                "○ " + stringRes(R.string.dash_enforcing_off)
            },
            fontSize = 20.sp,
            color = if (snapshot.enforcing) Ok else Warn,
        )
    }
}

/**
 * Both permissions are "special access" ones that cannot be requested with a dialog, so
 * the only thing the app can do is say what is missing and open the right settings screen.
 */
@Composable
private fun PermissionWarnings(resumeTick: Int) {
    val context = LocalContext.current
    val accessibilityOn = remember(resumeTick) { SystemAccess.isAccessibilityEnabled(context) }
    val usageOn = remember(resumeTick) { SystemAccess.isUsageAccessGranted(context) }

    if (!accessibilityOn) {
        Section(title = stringRes(R.string.warn_accessibility_title), accent = Danger) {
            Body(stringRes(R.string.warn_accessibility_body), TextPrimary, size = 18)
            Button(onClick = { context.startActivity(SystemAccess.accessibilitySettingsIntent()) }) {
                Text(stringRes(R.string.warn_accessibility_action), fontSize = 19.sp)
            }
        }
    }
    if (!usageOn) {
        Spacer(Modifier.height(if (accessibilityOn) 0.dp else 20.dp))
        Section(title = stringRes(R.string.warn_usage_title), accent = Warn) {
            Body(stringRes(R.string.warn_usage_body), TextPrimary, size = 18)
            Button(onClick = { context.startActivity(SystemAccess.usageAccessSettingsIntent()) }) {
                Text(stringRes(R.string.warn_usage_action), fontSize = 19.sp)
            }
        }
    }
}

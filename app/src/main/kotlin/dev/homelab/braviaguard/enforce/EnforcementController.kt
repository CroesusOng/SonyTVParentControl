package dev.homelab.braviaguard.enforce

import android.accessibilityservice.AccessibilityService
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import dev.homelab.braviaguard.R
import dev.homelab.braviaguard.core.Base32
import dev.homelab.braviaguard.core.BlockEventRequest
import dev.homelab.braviaguard.core.BlockReason
import dev.homelab.braviaguard.core.Decision
import dev.homelab.braviaguard.core.EnrollRequest
import dev.homelab.braviaguard.core.ForegroundSession
import dev.homelab.braviaguard.core.Grant
import dev.homelab.braviaguard.core.GrantSource
import dev.homelab.braviaguard.core.OfflineCodeGate
import dev.homelab.braviaguard.core.OfflineCodeResult
import dev.homelab.braviaguard.core.Policy
import dev.homelab.braviaguard.core.RuleEngine
import dev.homelab.braviaguard.core.SyncResponse
import dev.homelab.braviaguard.core.UsageLedger
import dev.homelab.braviaguard.core.UsageReportRequest
import dev.homelab.braviaguard.core.UsageSnapshot
import dev.homelab.braviaguard.core.UsageStore
import dev.homelab.braviaguard.core.toDto
import dev.homelab.braviaguard.core.toDtos
import dev.homelab.braviaguard.data.GuardStore
import dev.homelab.braviaguard.data.SecretStore
import dev.homelab.braviaguard.data.StoredState
import dev.homelab.braviaguard.net.BackendClient
import dev.homelab.braviaguard.usage.AppCatalog
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import java.io.IOException
import java.time.Duration
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.util.UUID

/** What the parent dashboard on the TV shows. Derived state only. */
data class GuardSnapshot(
    val paired: Boolean = false,
    val baseUrl: String? = null,
    val enforcing: Boolean = false,
    val policyVersion: Long = 0,
    val lastSyncMillis: Long = 0,
    val unreachableSinceMillis: Long = 0,
    val todaySeconds: Long = 0,
    val weekSeconds: Long = 0,
    val remainingTodaySeconds: Long? = null,
    val topAppsToday: List<Pair<String, Long>> = emptyList(),
    val blockedLabel: String? = null,
    val curfewCount: Int = 0,
    val activeGrantExpiryMillis: Long? = null,
)

/**
 * Owns the one [RuleEngine] instance and everything around it: tracking what is on screen,
 * charging time against budgets, talking to the Pi, and driving the overlay.
 *
 * Lives on the Application so the dashboard can read its state, but only does enforcement
 * while an accessibility service is attached.
 *
 * There is no foreground service here. An AccessibilityService is already a long-lived
 * system-bound service exempt from background execution limits, so the sync loop runs in
 * its lifetime and avoids the Android 14+ foregroundServiceType rules and the Android 15
 * dataSync daily cap entirely. WorkManager covers the case where the service is off.
 */
class EnforcementController(
    private val context: Context,
    private val store: GuardStore,
    private val secrets: SecretStore,
    private val backend: BackendClient,
    private val catalog: AppCatalog,
    private val zone: ZoneId = ZoneId.systemDefault(),
) {

    private val engine = RuleEngine(zone)
    private val ledger = UsageLedger(zone)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val writeLock = Mutex()
    private val main = Handler(Looper.getMainLooper())

    /**
     * Never blocked, whatever the policy says: the system UI, the launcher (we kick blocked
     * apps *to* it, so blocking it would loop), the settings app (a parent must be able to
     * turn this off), and this app itself.
     */
    private val baseExempt: Set<String> =
        Policy.SYSTEM_EXEMPT_HINTS + context.packageName + listOfNotNull(catalog.launcherPackage())

    @Volatile private var stored = StoredState()

    /**
     * The live usage map, and the authoritative copy of it.
     *
     * DataStore emits asynchronously, so reading `stored.usage` to merge a finished
     * session would race: two sessions closing within a few hundred milliseconds would
     * both read the pre-write map and the second write would drop the first. This app is
     * the only writer, so an in-memory copy seeded from the first emission is safe and
     * removes the race.
     */
    @Volatile private var usage: UsageStore = emptyMap()
    @Volatile private var usageLoaded = false

    /**
     * True while the debug-only preview overlay is up. The polling loop would otherwise
     * take it down within three seconds: nothing is actually blocked, so the next
     * evaluation returns Allow and Allow hides the overlay.
     */
    @Volatile private var previewing = false

    @Volatile private var currentPackage: String? = null
    @Volatile private var sessionStart: Instant? = null
    @Volatile private var blockedPackage: String? = null

    private var service: AccessibilityService? = null
    private var overlay: BlockOverlay? = null
    private var loopJob: Job? = null

    private val _snapshot = MutableStateFlow(GuardSnapshot())
    val snapshot: StateFlow<GuardSnapshot> = _snapshot.asStateFlow()

    init {
        scope.launch {
            store.state.collect { state ->
                stored = state
                if (!usageLoaded) {
                    usage = state.usage
                    usageLoaded = true
                }
                publish()
                // A policy or grant change can unblock what is on screen right now.
                reevaluate(Instant.now())
            }
        }
    }

    // ---------- lifecycle ----------

    fun attach(service: AccessibilityService, overlay: BlockOverlay) {
        this.service = service
        this.overlay = overlay
        overlay.onCodeSubmitted = { code -> submitEscapeCode(code) }
        overlay.onDismissed = {
            blockedPackage = null
            previewing = false
        }
        startLoop()
    }

    fun detach() {
        val now = Instant.now()
        currentPackage?.let { pkg -> sessionStart?.let { start -> closeSession(pkg, start, now) } }
        currentPackage = null
        sessionStart = null
        previewing = false
        overlay?.hide()
        overlay = null
        service = null
        loopJob?.cancel()
        loopJob = null
    }

    fun startLoop() {
        if (loopJob?.isActive == true) return
        loopJob = scope.launch { loop() }
    }

    // ---------- the signal from the accessibility service ----------

    fun onForegroundChanged(packageName: String) {
        val now = Instant.now()
        if (packageName == currentPackage) return

        val previous = currentPackage
        val start = sessionStart
        currentPackage = packageName
        sessionStart = now
        if (previous != null && start != null) closeSession(previous, start, now)

        applyDecision(packageName, now)
    }

    // ---------- decisions ----------

    private fun decide(packageName: String, now: Instant): Decision = engine.evaluate(
        packageName = packageName,
        policy = effectivePolicy(),
        usage = snapshotAt(now),
        grants = stored.grants,
        now = now,
        backendUnreachableSince = unreachableSince(),
    )

    private fun applyDecision(packageName: String, now: Instant) {
        when (val decision = decide(packageName, now)) {
            is Decision.Allow -> {
                // Only clear the overlay for the app it was raised about. Kicking a blocked
                // app to the home screen makes the launcher the foreground app, and the
                // launcher is always allowed — that must not dismiss the message.
                if (!previewing && (blockedPackage == null || blockedPackage == packageName)) {
                    blockedPackage = null
                    overlay?.hide()
                }
            }

            is Decision.Block -> block(packageName, decision, now)
        }
        publish()
    }

    private fun block(packageName: String, decision: Decision.Block, now: Instant) {
        // A real verdict takes the screen back from a preview that was left up.
        previewing = false
        blockedPackage = packageName
        val presentation = BlockMessages.present(
            context = context,
            block = decision,
            appLabel = catalog.label(packageName),
            zone = zone,
            now = now,
            offline = unreachableSince() != null,
        )
        main.post {
            // Kick first, then cover: GLOBAL_ACTION_HOME is what actually stops playback.
            service?.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME)
            overlay?.show(presentation)
        }
        scope.launch { reportBlock(packageName, decision, now) }
    }

    private fun reevaluate(now: Instant) {
        blockedPackage?.let { blocked ->
            if (decide(blocked, now).allowed) {
                blockedPackage = null
                overlay?.hide()
            }
        }
        val pkg = currentPackage ?: return
        if (blockedPackage == null) applyDecision(pkg, now)
    }

    // ---------- debug ----------

    /**
     * Puts the block overlay on screen with invented facts, for the debug build only.
     *
     * The two riskiest things in this app — whether the accessibility overlay really
     * draws above a running video app, and whether it takes the D-pad — can only be
     * answered on a real television, and waiting for a real block to find out means
     * waiting until the backend, the domain and the pairing are all done. This answers
     * both with nothing but `adb install`.
     *
     * Deliberately not a real block: no GLOBAL_ACTION_HOME, which would throw you out
     * of this very screen the moment you pressed the button, and no block report to the
     * Pi, which would put a fake entry in the parent app's history.
     *
     * Returns false when the accessibility service is not running, which is the usual
     * reason nothing happens and is otherwise invisible from this screen.
     */
    fun previewBlockOverlay(): Boolean {
        val target = overlay ?: return false
        val now = Instant.now()
        val presentation = BlockMessages.present(
            context = context,
            block = Decision.Block(
                reason = BlockReason.DAILY_BUDGET_EXHAUSTED,
                limitMinutes = PREVIEW_LIMIT_MINUTES,
                usedSeconds = PREVIEW_LIMIT_MINUTES * 60L,
                // Tomorrow's local midnight, the same instant a real daily budget resets,
                // so the "try again at" line is formatted exactly as it would be for real.
                retryAt = now.atZone(zone).toLocalDate().plusDays(1)
                    .atStartOfDay(zone).toInstant(),
            ),
            appLabel = context.getString(R.string.debug_preview_app),
            zone = zone,
            now = now,
            offline = unreachableSince() != null,
        )
        previewing = true
        main.post { target.show(presentation) }
        return true
    }

    // ---------- offline escape code ----------

    fun submitEscapeCode(code: String) {
        val secretBase32 = secrets.get(SecretStore.KEY_ESCAPE_SECRET)
        if (secretBase32 == null) {
            overlay?.showCodeResult(context.getString(R.string.block_code_unavailable), false)
            return
        }
        val secret = runCatching { Base32.decode(secretBase32) }.getOrNull()
        if (secret == null) {
            overlay?.showCodeResult(context.getString(R.string.block_code_unavailable), false)
            return
        }

        val gate = OfflineCodeGate(secret, lastAcceptedCounter = stored.offlineCounter)
        when (gate.redeem(code, System.currentTimeMillis())) {
            OfflineCodeResult.ACCEPTED -> {
                val now = Instant.now()
                val grant = Grant(
                    id = "offline-${UUID.randomUUID()}",
                    source = GrantSource.OFFLINE_CODE,
                    issuedAt = now,
                    expiresAt = now.plus(Duration.ofMinutes(OFFLINE_GRANT_MINUTES)),
                    note = "offline escape code",
                )
                blockedPackage = null
                scope.launch {
                    writeLock.withLock {
                        store.saveOfflineCounter(gate.lastAcceptedCounter)
                        store.saveGrants((stored.grants + grant).map { it.toDto() })
                    }
                }
                overlay?.showCodeResult(
                    context.getString(R.string.block_code_ok, OFFLINE_GRANT_MINUTES.toInt()),
                    true,
                )
            }

            OfflineCodeResult.ALREADY_USED ->
                overlay?.showCodeResult(context.getString(R.string.block_code_used), false)

            OfflineCodeResult.WRONG_CODE ->
                overlay?.showCodeResult(context.getString(R.string.block_code_wrong), false)
        }
    }

    // ---------- pairing ----------

    suspend fun pair(baseUrl: String, pairingCode: String): Result<Unit> = runCatching {
        val normalised = BackendClient.normalise(baseUrl)
        val response = backend.enroll(
            normalised,
            EnrollRequest(
                pairingCode = pairingCode.trim(),
                deviceName = Build.MODEL.ifBlank { "Android TV" },
                model = "${Build.MANUFACTURER} ${Build.MODEL}".trim(),
                androidRelease = Build.VERSION.RELEASE.orEmpty(),
            ),
        )
        secrets.put(SecretStore.KEY_DEVICE_TOKEN, response.token)
        secrets.put(SecretStore.KEY_ESCAPE_SECRET, response.escapeSecretBase32)
        store.savePairing(normalised, response.deviceId)
        syncOnce()
    }

    /**
     * Unpairing is gated on an escape code, not left open. Otherwise the easiest way round
     * the whole app would be to open it and press "unpair".
     */
    suspend fun unpair(code: String): Boolean {
        val secretBase32 = secrets.get(SecretStore.KEY_ESCAPE_SECRET) ?: return false
        val secret = runCatching { Base32.decode(secretBase32) }.getOrNull() ?: return false
        val gate = OfflineCodeGate(secret, lastAcceptedCounter = stored.offlineCounter)
        if (gate.redeem(code, System.currentTimeMillis()) != OfflineCodeResult.ACCEPTED) return false

        writeLock.withLock {
            store.saveOfflineCounter(gate.lastAcceptedCounter)
            store.clearPairing()
        }
        secrets.remove(SecretStore.KEY_DEVICE_TOKEN)
        secrets.remove(SecretStore.KEY_ESCAPE_SECRET)
        blockedPackage = null
        overlay?.hide()
        return true
    }

    // ---------- the loop ----------

    private suspend fun loop() {
        while (currentCoroutineContext().isActive) {
            syncOnce()
            val now = Instant.now()
            flushSession(now)
            reevaluate(now)
            publish()
            delay(nextDelayMillis(now))
        }
    }

    suspend fun syncOnce() {
        val state = stored
        val baseUrl = state.baseUrl ?: return
        val deviceId = state.deviceId ?: return
        try {
            applySync(backend.sync(baseUrl, deviceId, state.policy.version))
            store.markSynced(System.currentTimeMillis())
            runCatching { pushUsage(baseUrl, deviceId) }
        } catch (e: IOException) {
            Log.i(TAG, "backend unreachable: ${e.message}")
            store.markUnreachable(System.currentTimeMillis())
        }
    }

    private suspend fun applySync(response: SyncResponse) = writeLock.withLock {
        response.policy?.let { store.savePolicy(it) }
        response.escapeSecretBase32?.let { secrets.put(SecretStore.KEY_ESCAPE_SECRET, it) }
        // Grants redeemed from an offline code exist only on the TV; the backend has never
        // heard of them, so its list must not wipe them out while they are still live.
        val now = Instant.now()
        val localGrants = stored.grants.filter {
            it.source == GrantSource.OFFLINE_CODE && it.expiresAt.isAfter(now)
        }
        store.saveGrants(response.grants + localGrants.map { it.toDto() })
    }

    private suspend fun pushUsage(baseUrl: String, deviceId: String) {
        val today = LocalDate.now(zone)
        // Only the days that can still change; the backend keeps the history.
        val recent = liveUsage(Instant.now()).filterKeys { !it.isBefore(today.minusDays(2)) }
        if (recent.isEmpty()) return
        // Labels ride along so the parent app can show "Netflix" and offer a pick list,
        // instead of asking anyone to type com.google.android.youtube.tv by hand.
        val labels = recent.values
            .flatMap { it.keys }
            .distinct()
            .associateWith { catalog.label(it) }
        backend.reportUsage(
            baseUrl,
            UsageReportRequest(deviceId, recent.toDtos(), labels),
        )
    }

    private suspend fun reportBlock(packageName: String, decision: Decision.Block, now: Instant) {
        val baseUrl = stored.baseUrl ?: return
        val deviceId = stored.deviceId ?: return
        runCatching {
            backend.reportBlock(
                baseUrl,
                BlockEventRequest(
                    deviceId = deviceId,
                    packageName = packageName,
                    appLabel = catalog.label(packageName),
                    reason = decision.reason.name,
                    atMillis = now.toEpochMilli(),
                ),
            )
        }
    }

    private fun nextDelayMillis(now: Instant): Long {
        val base = when {
            // The parent has just been told to unlock from their phone; make it feel instant.
            overlay?.isVisible == true -> 3_000L
            stored.policy.enforcementEnabled -> 30_000L
            else -> 120_000L
        }
        val boundary = engine.nextBoundary(
            policy = effectivePolicy(),
            usage = snapshotAt(now),
            grants = stored.grants,
            now = now,
            foregroundPackage = currentPackage,
            backendUnreachableSince = unreachableSince(),
        )?.let { Duration.between(now, it).toMillis().coerceAtLeast(1_000L) }

        return minOf(base, boundary ?: Long.MAX_VALUE)
    }

    // ---------- usage accounting ----------

    /**
     * Rolls the in-flight session into persistent storage without ending it, so a budget
     * trips mid-session instead of only when the child switches apps.
     */
    private fun flushSession(now: Instant) {
        val pkg = currentPackage ?: return
        val start = sessionStart ?: return
        if (Duration.between(start, now).seconds < 1) return
        sessionStart = now
        closeSession(pkg, start, now)
    }

    private fun closeSession(packageName: String, start: Instant, end: Instant) {
        if (!end.isAfter(start)) return
        scope.launch {
            writeLock.withLock {
                val merged = ledger.record(usage, ForegroundSession(packageName, start, end))
                usage = ledger.prune(merged, end)
                store.saveUsage(usage)
            }
        }
    }

    /** Persisted usage plus whatever the current session has accrued so far. */
    private fun liveUsage(now: Instant): UsageStore {
        val pkg = currentPackage ?: return usage
        val start = sessionStart ?: return usage
        if (!now.isAfter(start)) return usage
        return ledger.record(usage, ForegroundSession(pkg, start, now))
    }

    private fun snapshotAt(now: Instant): UsageSnapshot = ledger.snapshot(liveUsage(now), now)

    // ---------- derived state ----------

    private fun effectivePolicy(): Policy {
        val policy = stored.policy
        return policy.copy(exemptPackages = policy.exemptPackages + baseExempt)
    }

    private fun unreachableSince(): Instant? =
        stored.unreachableSinceMillis.takeIf { it > 0L }?.let(Instant::ofEpochMilli)

    private fun publish() {
        val now = Instant.now()
        val policy = effectivePolicy()
        val usage = snapshotAt(now)
        val exempt = policy.exemptPackages
        val remaining = currentPackage
            ?.let { (decide(it, now) as? Decision.Allow)?.remaining?.tightestSeconds }

        _snapshot.value = GuardSnapshot(
            paired = stored.isPaired,
            baseUrl = stored.baseUrl,
            enforcing = policy.enforcementEnabled,
            policyVersion = policy.version,
            lastSyncMillis = stored.lastSyncMillis,
            unreachableSinceMillis = stored.unreachableSinceMillis,
            todaySeconds = usage.todayCountableSeconds(exempt),
            weekSeconds = usage.weekCountableSeconds(exempt),
            remainingTodaySeconds = remaining,
            topAppsToday = usage.todayPerAppSeconds
                .filterKeys { it !in exempt }
                .entries
                .sortedByDescending { it.value }
                .take(6)
                .map { catalog.label(it.key) to it.value },
            blockedLabel = blockedPackage?.let { catalog.label(it) },
            curfewCount = policy.curfews.size,
            activeGrantExpiryMillis = stored.grants
                .filter { it.isActiveAt(now) }
                .maxOfOrNull { it.expiresAt.toEpochMilli() },
        )
    }

    private companion object {
        const val TAG = "Enforcement"
        /** Arbitrary, but a plausible household number reads better than 1 or 999. */
        const val PREVIEW_LIMIT_MINUTES = 120
        const val OFFLINE_GRANT_MINUTES = 30L
    }
}

package dev.homelab.braviaguard

import android.app.Application
import android.content.Context
import dev.homelab.braviaguard.data.GuardStore
import dev.homelab.braviaguard.data.SecretStore
import dev.homelab.braviaguard.enforce.EnforcementController
import dev.homelab.braviaguard.net.BackendClient
import dev.homelab.braviaguard.net.BackendDiscovery
import dev.homelab.braviaguard.sync.SyncWorker
import dev.homelab.braviaguard.usage.AppCatalog
import dev.homelab.braviaguard.usage.UsageStatsReader
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import java.time.ZoneId

/**
 * Hand-rolled service locator. One screen, one service and one worker is not enough
 * surface to earn a DI framework and its build-time cost.
 */
class GuardApp : Application() {

    private val appScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    val zone: ZoneId get() = ZoneId.systemDefault()

    val store by lazy { GuardStore(this) }
    val secrets by lazy { SecretStore(this) }
    val catalog by lazy { AppCatalog(this) }
    val backend by lazy { BackendClient(secrets) }
    val discovery by lazy { BackendDiscovery(this) }
    val usageStats by lazy { UsageStatsReader(this, zone) }

    val enforcement by lazy {
        EnforcementController(this, store, secrets, backend, catalog, zone)
    }

    override fun onCreate() {
        super.onCreate()
        // Start the sync loop with the process rather than waiting for the accessibility
        // service, so the dashboard has fresh data even while enforcement is off.
        enforcement.startLoop()
        SyncWorker.schedule(this)
    }

    fun recordAccessibilityConnected() {
        appScope.launch { store.setAccessibilityWasEnabled(true) }
    }

    companion object {
        fun from(context: Context): GuardApp = context.applicationContext as GuardApp
    }
}

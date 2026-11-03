package dev.homelab.braviaguard.boot

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import dev.homelab.braviaguard.sync.SyncWorker

/**
 * A BRAVIA is unplugged and replugged often enough that this matters. The accessibility
 * service is restarted by the system on its own if it is enabled; all this has to do is
 * put the WorkManager backstop back, since periodic work does not survive a reinstall.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED ->
                SyncWorker.schedule(context)
        }
    }
}

package dev.homelab.braviaguard.sync

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import dev.homelab.braviaguard.GuardApp
import java.util.concurrent.TimeUnit

/**
 * The backstop, not the main sync path.
 *
 * While the accessibility service is connected its own loop polls the Pi every few seconds
 * to 30 seconds. This exists for the gap: the service switched off, the TV just rebooted
 * and nothing has opened the app yet, or the process was killed. 15 minutes is the shortest
 * period WorkManager honours for periodic work.
 */
class SyncWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        GuardApp.from(applicationContext).enforcement.syncOnce()
        return Result.success()
    }

    companion object {
        private const val UNIQUE_NAME = "braviaguard-sync"

        fun schedule(context: Context) {
            val request = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
                .setConstraints(
                    Constraints.Builder()
                        .setRequiredNetworkType(NetworkType.CONNECTED)
                        .build(),
                )
                .build()
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                UNIQUE_NAME,
                ExistingPeriodicWorkPolicy.KEEP,
                request,
            )
        }
    }
}

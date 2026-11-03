package dev.homelab.braviaguard.usage

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import java.util.concurrent.ConcurrentHashMap

data class AppEntry(val packageName: String, val label: String)

/**
 * Package name -> something a parent will recognise on screen.
 * Lookups happen on the accessibility event path, so results are cached.
 */
class AppCatalog(context: Context) {

    private val packageManager: PackageManager = context.packageManager
    private val labels = ConcurrentHashMap<String, String>()

    fun label(packageName: String): String = labels.getOrPut(packageName) {
        runCatching {
            packageManager.getApplicationLabel(
                packageManager.getApplicationInfo(packageName, 0),
            ).toString()
        }.getOrDefault(packageName)
    }

    /** Everything a child can actually start from the TV home screen. */
    fun launchableApps(): List<AppEntry> {
        val seen = linkedMapOf<String, AppEntry>()
        listOf(Intent.CATEGORY_LEANBACK_LAUNCHER, Intent.CATEGORY_LAUNCHER).forEach { category ->
            val intent = Intent(Intent.ACTION_MAIN).addCategory(category)
            packageManager.queryIntentActivities(intent, 0).forEach { resolved ->
                val pkg = resolved.activityInfo.packageName
                if (pkg !in seen) {
                    val label = resolved.loadLabel(packageManager).toString()
                    labels[pkg] = label
                    seen[pkg] = AppEntry(pkg, label)
                }
            }
        }
        return seen.values.sortedBy { it.label.lowercase() }
    }

    /** The home screen package, which must never be blocked or there is nowhere to land. */
    fun launcherPackage(): String? {
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME)
        return packageManager.resolveActivity(intent, PackageManager.MATCH_DEFAULT_ONLY)
            ?.activityInfo?.packageName
    }
}

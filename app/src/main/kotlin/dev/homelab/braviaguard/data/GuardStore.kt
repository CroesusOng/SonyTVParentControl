package dev.homelab.braviaguard.data

import android.content.Context
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.longPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import dev.homelab.braviaguard.core.DayUsageDto
import dev.homelab.braviaguard.core.GrantDto
import dev.homelab.braviaguard.core.Policy
import dev.homelab.braviaguard.core.PolicyDto
import dev.homelab.braviaguard.core.ProtocolJson
import dev.homelab.braviaguard.core.UsageStore
import dev.homelab.braviaguard.core.toDomain
import dev.homelab.braviaguard.core.toDtos
import dev.homelab.braviaguard.core.toUsageStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import kotlinx.serialization.builtins.ListSerializer

private val Context.guardDataStore by preferencesDataStore(name = "guard")

/**
 * Everything the TV remembers between reboots, except the two secrets (see [SecretStore]).
 *
 * DataStore rather than Room on purpose. The whole working set is one policy document, a
 * handful of live grants, and a map of `date -> package -> seconds` capped at 90 days —
 * a few kilobytes that is always read and written whole. Room would add a KSP processor,
 * a schema to migrate and a DAO layer to earn nothing back.
 */
class GuardStore(private val context: Context) {

    val state: Flow<StoredState> = context.guardDataStore.data.map { it.toState() }

    suspend fun savePolicy(policy: PolicyDto) = context.guardDataStore.edit {
        it[KEY_POLICY] = ProtocolJson.encodeToString(PolicyDto.serializer(), policy)
    }

    suspend fun saveGrants(grants: List<GrantDto>) = context.guardDataStore.edit {
        it[KEY_GRANTS] = ProtocolJson.encodeToString(ListSerializer(GrantDto.serializer()), grants)
    }

    suspend fun saveUsage(usage: UsageStore) = context.guardDataStore.edit {
        it[KEY_USAGE] = ProtocolJson.encodeToString(
            ListSerializer(DayUsageDto.serializer()),
            usage.toDtos(),
        )
    }

    suspend fun savePairing(baseUrl: String, deviceId: String) = context.guardDataStore.edit {
        it[KEY_BASE_URL] = baseUrl
        it[KEY_DEVICE_ID] = deviceId
    }

    suspend fun clearPairing() = context.guardDataStore.edit {
        it.remove(KEY_BASE_URL)
        it.remove(KEY_DEVICE_ID)
        it.remove(KEY_POLICY)
        it.remove(KEY_GRANTS)
        it[KEY_UNREACHABLE_SINCE] = 0L
    }

    suspend fun markSynced(atMillis: Long) = context.guardDataStore.edit {
        it[KEY_LAST_SYNC] = atMillis
        it[KEY_UNREACHABLE_SINCE] = 0L
    }

    /** Records the first failure of a run; later failures must not move the timestamp. */
    suspend fun markUnreachable(atMillis: Long) = context.guardDataStore.edit {
        if ((it[KEY_UNREACHABLE_SINCE] ?: 0L) == 0L) {
            it[KEY_UNREACHABLE_SINCE] = atMillis
        }
    }

    suspend fun saveOfflineCounter(counter: Long) = context.guardDataStore.edit {
        it[KEY_OFFLINE_COUNTER] = counter
    }

    suspend fun setUsageReportedThrough(day: String) = context.guardDataStore.edit {
        it[KEY_USAGE_REPORTED_THROUGH] = day
    }

    suspend fun setAccessibilityWasEnabled(enabled: Boolean) = context.guardDataStore.edit {
        it[KEY_ACCESSIBILITY_SEEN] = enabled
    }

    private fun Preferences.toState(): StoredState {
        val policyJson = this[KEY_POLICY]
        val grantsJson = this[KEY_GRANTS]
        val usageJson = this[KEY_USAGE]
        return StoredState(
            policy = policyJson
                ?.let { runCatching { ProtocolJson.decodeFromString(PolicyDto.serializer(), it) }.getOrNull() }
                ?.toDomain()
                ?: Policy.UNCONFIGURED,
            grants = grantsJson
                ?.let {
                    runCatching {
                        ProtocolJson.decodeFromString(ListSerializer(GrantDto.serializer()), it)
                    }.getOrNull()
                }
                ?.map { it.toDomain() }
                .orEmpty(),
            usage = usageJson
                ?.let {
                    runCatching {
                        ProtocolJson.decodeFromString(ListSerializer(DayUsageDto.serializer()), it)
                    }.getOrNull()
                }
                ?.toUsageStore()
                .orEmpty(),
            baseUrl = this[KEY_BASE_URL],
            deviceId = this[KEY_DEVICE_ID],
            lastSyncMillis = this[KEY_LAST_SYNC] ?: 0L,
            unreachableSinceMillis = this[KEY_UNREACHABLE_SINCE] ?: 0L,
            offlineCounter = this[KEY_OFFLINE_COUNTER] ?: -1L,
            usageReportedThrough = this[KEY_USAGE_REPORTED_THROUGH],
            accessibilityWasEnabled = this[KEY_ACCESSIBILITY_SEEN] ?: false,
        )
    }

    private companion object {
        val KEY_POLICY = stringPreferencesKey("policy_json")
        val KEY_GRANTS = stringPreferencesKey("grants_json")
        val KEY_USAGE = stringPreferencesKey("usage_json")
        val KEY_BASE_URL = stringPreferencesKey("backend_base_url")
        val KEY_DEVICE_ID = stringPreferencesKey("device_id")
        val KEY_LAST_SYNC = longPreferencesKey("last_sync_millis")
        val KEY_UNREACHABLE_SINCE = longPreferencesKey("unreachable_since_millis")
        val KEY_OFFLINE_COUNTER = longPreferencesKey("offline_counter")
        val KEY_USAGE_REPORTED_THROUGH = stringPreferencesKey("usage_reported_through")
        val KEY_ACCESSIBILITY_SEEN = booleanPreferencesKey("accessibility_was_enabled")
    }
}

data class StoredState(
    val policy: Policy = Policy.UNCONFIGURED,
    val grants: List<dev.homelab.braviaguard.core.Grant> = emptyList(),
    val usage: UsageStore = emptyMap(),
    val baseUrl: String? = null,
    val deviceId: String? = null,
    val lastSyncMillis: Long = 0L,
    val unreachableSinceMillis: Long = 0L,
    val offlineCounter: Long = -1L,
    val usageReportedThrough: String? = null,
    /** Lets the dashboard point out that enforcement was switched off in TV settings. */
    val accessibilityWasEnabled: Boolean = false,
) {
    val isPaired: Boolean get() = !baseUrl.isNullOrBlank() && !deviceId.isNullOrBlank()
}

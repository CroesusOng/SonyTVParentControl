package dev.homelab.braviaguard.net

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.util.Log
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow

data class DiscoveredBackend(
    val name: String,
    val host: String,
    val port: Int,
    val secure: Boolean,
) {
    val baseUrl: String get() = "${if (secure) "https" else "http"}://$host:$port"
}

/**
 * Finds the Pi over mDNS so nobody has to spell out a URL with a D-pad.
 *
 * The backend itself publishes nothing; the Pi's existing avahi-daemon advertises
 * `_braviaguard._tcp` from a service file we ship in server/deploy. That keeps the Go
 * binary dependency-free and reuses the mDNS responder a Raspberry Pi already runs.
 * Manual entry stays available for anyone whose network blocks multicast.
 */
class BackendDiscovery(context: Context) {

    private val nsd = context.getSystemService(Context.NSD_SERVICE) as NsdManager

    @Suppress("DEPRECATION") // resolveService's replacement is API 34+; minSdk here is 26.
    fun discover(): Flow<List<DiscoveredBackend>> = callbackFlow {
        val found = linkedMapOf<String, DiscoveredBackend>()
        // One resolve at a time: NsdManager rejects concurrent resolveService calls.
        val pending = ArrayDeque<NsdServiceInfo>()
        var resolving = false

        fun resolveNext() {
            if (resolving) return
            val next = pending.removeFirstOrNull() ?: return
            resolving = true
            nsd.resolveService(
                next,
                object : NsdManager.ResolveListener {
                    override fun onResolveFailed(info: NsdServiceInfo, errorCode: Int) {
                        Log.w(TAG, "resolve failed for ${info.serviceName}: $errorCode")
                        resolving = false
                        resolveNext()
                    }

                    override fun onServiceResolved(info: NsdServiceInfo) {
                        val host = info.host?.hostAddress
                        if (host != null) {
                            val secure = info.attributes["scheme"]
                                ?.decodeToString()
                                ?.equals("https", ignoreCase = true)
                                ?: (info.port == 443 || info.port == 8443)
                            found[info.serviceName] =
                                DiscoveredBackend(info.serviceName, host, info.port, secure)
                            trySend(found.values.toList())
                        }
                        resolving = false
                        resolveNext()
                    }
                },
            )
        }

        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) = Unit

            override fun onServiceFound(info: NsdServiceInfo) {
                pending.addLast(info)
                resolveNext()
            }

            override fun onServiceLost(info: NsdServiceInfo) {
                if (found.remove(info.serviceName) != null) trySend(found.values.toList())
            }

            override fun onDiscoveryStopped(serviceType: String) = Unit

            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                Log.w(TAG, "discovery failed to start: $errorCode")
                close()
            }

            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) = Unit
        }

        trySend(emptyList())
        nsd.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener)
        awaitClose {
            runCatching { nsd.stopServiceDiscovery(listener) }
        }
    }

    private companion object {
        const val TAG = "BackendDiscovery"
        const val SERVICE_TYPE = "_braviaguard._tcp."
    }
}

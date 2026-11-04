package dev.homelab.braviaguard.net

import dev.homelab.braviaguard.core.BlockEventRequest
import dev.homelab.braviaguard.core.EnrollRequest
import dev.homelab.braviaguard.core.EnrollResponse
import dev.homelab.braviaguard.core.ProtocolJson
import dev.homelab.braviaguard.core.SyncResponse
import dev.homelab.braviaguard.core.UsageReportRequest
import dev.homelab.braviaguard.data.SecretStore
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.KSerializer
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException
import java.util.concurrent.TimeUnit

class BackendException(message: String, cause: Throwable? = null) : IOException(message, cause)

/**
 * Talks to the Go backend on the Raspberry Pi.
 *
 * Plain request/response rather than SSE or a WebSocket, even for the push path. Pushing
 * is done by holding an ordinary GET open on the backend (`wait` on /api/device/sync), so
 * a parent's unlock arrives in one LAN round trip while every reply is still just a reply:
 * a held request that dies in standby is a failed request, retried, with no reconnect state
 * machine to get wrong across the standby/resume cycles a TV goes through daily.
 * See EnforcementController.watchLoop, and handleSync on the server.
 */
class BackendClient(private val secrets: SecretStore) {

    private val http = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .writeTimeout(10, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    /**
     * A second client for the one call that is meant to sit there: the held sync request.
     * Its read timeout has to outlast the server's hold, and raising it on the shared
     * client would leave every ordinary call waiting the better part of a minute on a
     * connection that has silently gone away. newBuilder shares the connection pool and
     * dispatcher, so this is a different timeout, not a second stack of sockets.
     */
    private val waiting = http.newBuilder()
        .readTimeout(MAX_WAIT_SECONDS + 15L, TimeUnit.SECONDS)
        .build()

    suspend fun enroll(baseUrl: String, request: EnrollRequest): EnrollResponse =
        post(baseUrl, "/api/device/enroll", EnrollRequest.serializer(), request,
            EnrollResponse.serializer(), authenticated = false)

    /**
     * [waitSeconds] above zero asks the backend to hold the request until something
     * changes, up to that many seconds. Zero replies at once.
     */
    suspend fun sync(
        baseUrl: String,
        deviceId: String,
        policyVersion: Long,
        revision: Long = 0,
        waitSeconds: Int = 0,
    ): SyncResponse = get(
        baseUrl,
        "/api/device/sync?deviceId=$deviceId&policyVersion=$policyVersion" +
            "&revision=$revision&wait=$waitSeconds",
        SyncResponse.serializer(),
        client = if (waitSeconds > 0) waiting else http,
    )

    suspend fun reportUsage(baseUrl: String, request: UsageReportRequest) {
        postUnit(baseUrl, "/api/device/usage", UsageReportRequest.serializer(), request)
    }

    suspend fun reportBlock(baseUrl: String, request: BlockEventRequest) {
        postUnit(baseUrl, "/api/device/block-event", BlockEventRequest.serializer(), request)
    }

    private suspend fun <T> get(
        baseUrl: String,
        path: String,
        out: KSerializer<T>,
        client: OkHttpClient = http,
    ): T = execute(requestBuilder(baseUrl, path, authenticated = true).get().build(), out, client)

    private suspend fun <In, Out> post(
        baseUrl: String,
        path: String,
        inSerializer: KSerializer<In>,
        body: In,
        out: KSerializer<Out>,
        authenticated: Boolean = true,
    ): Out = execute(
        requestBuilder(baseUrl, path, authenticated)
            .post(ProtocolJson.encodeToString(inSerializer, body).toRequestBody(JSON))
            .build(),
        out,
    )

    private suspend fun <In> postUnit(
        baseUrl: String,
        path: String,
        inSerializer: KSerializer<In>,
        body: In,
    ) = withContext(Dispatchers.IO) {
        val request = requestBuilder(baseUrl, path, authenticated = true)
            .post(ProtocolJson.encodeToString(inSerializer, body).toRequestBody(JSON))
            .build()
        http.newCall(request).execute().use { response ->
            if (!response.isSuccessful) throw BackendException(describe(response.code, response.body?.string()))
        }
    }

    private suspend fun <T> execute(
        request: Request,
        out: KSerializer<T>,
        client: OkHttpClient = http,
    ): T =
        withContext(Dispatchers.IO) {
            try {
                client.newCall(request).execute().use { response ->
                    val text = response.body?.string().orEmpty()
                    if (!response.isSuccessful) throw BackendException(describe(response.code, text))
                    ProtocolJson.decodeFromString(out, text)
                }
            } catch (e: BackendException) {
                throw e
            } catch (e: Exception) {
                throw BackendException(e.message ?: e.javaClass.simpleName, e)
            }
        }

    private fun requestBuilder(baseUrl: String, path: String, authenticated: Boolean): Request.Builder {
        val builder = Request.Builder().url(normalise(baseUrl) + path)
        if (authenticated) {
            secrets.get(SecretStore.KEY_DEVICE_TOKEN)?.let {
                builder.header("Authorization", "Bearer $it")
            }
        }
        return builder
    }

    private fun describe(code: Int, body: String?): String {
        val detail = body
            ?.takeIf { it.isNotBlank() }
            ?.let {
                runCatching {
                    ProtocolJson.decodeFromString(
                        dev.homelab.braviaguard.core.ApiError.serializer(), it,
                    ).error
                }.getOrNull() ?: it.take(120)
            }
        return if (detail == null) "HTTP $code" else "HTTP $code: $detail"
    }

    companion object {
        private val JSON = "application/json; charset=utf-8".toMediaType()

        /**
         * How long to ask the backend to hold a sync request. Must not exceed the server's
         * own cap, or the server would simply clamp it and the client's read timeout would
         * be sized for a hold that never happens: see api.MaxSyncWait in device.go.
         */
        const val MAX_WAIT_SECONDS = 20

        /** Accepts what a person types on a remote: "pi5.local:8443", with or without scheme. */
        fun normalise(baseUrl: String): String {
            val trimmed = baseUrl.trim().trimEnd('/')
            return when {
                trimmed.startsWith("http://") || trimmed.startsWith("https://") -> trimmed
                else -> "https://$trimmed"
            }
        }
    }
}

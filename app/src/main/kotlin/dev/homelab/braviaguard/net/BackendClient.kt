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
 * Plain request/response polling rather than SSE or a WebSocket. A television is mains
 * powered so the battery argument for push does not apply, and polling has no reconnect
 * state machine to get wrong across the standby/resume cycles a TV goes through daily.
 * The caller varies the interval instead: see EnforcementController.
 */
class BackendClient(private val secrets: SecretStore) {

    private val http = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .writeTimeout(10, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    suspend fun enroll(baseUrl: String, request: EnrollRequest): EnrollResponse =
        post(baseUrl, "/api/device/enroll", EnrollRequest.serializer(), request,
            EnrollResponse.serializer(), authenticated = false)

    suspend fun sync(baseUrl: String, deviceId: String, policyVersion: Long): SyncResponse =
        get(baseUrl, "/api/device/sync?deviceId=$deviceId&policyVersion=$policyVersion",
            SyncResponse.serializer())

    suspend fun reportUsage(baseUrl: String, request: UsageReportRequest) {
        postUnit(baseUrl, "/api/device/usage", UsageReportRequest.serializer(), request)
    }

    suspend fun reportBlock(baseUrl: String, request: BlockEventRequest) {
        postUnit(baseUrl, "/api/device/block-event", BlockEventRequest.serializer(), request)
    }

    private suspend fun <T> get(baseUrl: String, path: String, out: KSerializer<T>): T =
        execute(requestBuilder(baseUrl, path, authenticated = true).get().build(), out)

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

    private suspend fun <T> execute(request: Request, out: KSerializer<T>): T =
        withContext(Dispatchers.IO) {
            try {
                http.newCall(request).execute().use { response ->
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

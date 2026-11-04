package io.aether.android

import android.content.Context
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import org.json.JSONException
import org.json.JSONObject

/**
 * The Edge gateway's `/?token=` URL while this process runs one, else null.
 * Set and cleared by MainActivity, which owns the gateway; read by whatever
 * in the process calls the server, on any thread.
 */
object LiveGateway {
    @Volatile var url: String? = null
}

class ServerCallException(message: String) : IOException(message)

/**
 * Calls [method] on the member's server, by the same route and with the same
 * identity as the dashboard in this app: `POST /api/v1/<method>` on the
 * tailnet dashboard, where the server identifies this phone's tailnet login
 * (docs/security.md), or on the running Edge gateway with its bearer token.
 * The app holds no credential of its own, so in Aether Edge mode with no
 * gateway running there is nothing to call with.
 *
 * Blocking; never on the main thread.
 */
fun Context.callServer(method: String, params: JSONObject, timeoutMs: Int = 15_000): JSONObject {
    val dashboard =
        when (connectionMode()) {
            ConnectionMode.TAILSCALE -> storedDashboardUrl()
            ConnectionMode.EDGE ->
                LiveGateway.url ?: throw ServerCallException("Aether is not connected through Aether Edge right now. Open the app and try again.")
            null -> null
        } ?: throw ServerCallException("The app is not connected to a server.")
    return callDashboard(dashboard, method, params, timeoutMs)
}

/** [callServer] on a tailnet [dashboard] that may no longer be the chosen one. */
fun callDashboard(dashboard: String, method: String, params: JSONObject, timeoutMs: Int = 15_000): JSONObject {
    val (address, token) = rpcEndpoint(dashboard, method)
    val connection = URL(address).openConnection() as HttpURLConnection
    try {
        connection.requestMethod = "POST"
        // The identity is this request's source or token; a redirect would
        // carry the body somewhere else.
        connection.instanceFollowRedirects = false
        connection.connectTimeout = timeoutMs
        connection.readTimeout = timeoutMs
        connection.doOutput = true
        connection.setRequestProperty("Content-Type", "application/json")
        token?.let { connection.setRequestProperty("Authorization", "Bearer $it") }
        connection.outputStream.use { it.write(params.toString().toByteArray(Charsets.UTF_8)) }
        val status = connection.responseCode
        val stream = if (status in 200..299) connection.inputStream else connection.errorStream
        val body = stream?.use { it.readBytes().toString(Charsets.UTF_8) }.orEmpty()
        if (status !in 200..299) throw ServerCallException(refusal(body) ?: "HTTP $status from $method")
        return try {
            JSONObject(body.ifEmpty { "{}" })
        } catch (_: JSONException) {
            throw ServerCallException("$method answered with something that is not JSON")
        }
    } finally {
        connection.disconnect()
    }
}

private fun refusal(body: String): String? =
    try {
        JSONObject(body).optJSONObject("error")?.optString("message")?.takeIf { it.isNotEmpty() }
    } catch (_: JSONException) {
        null
    }

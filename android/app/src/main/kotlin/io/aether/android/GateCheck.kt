package io.aether.android

import java.io.IOException

/**
 * The fields of the Edge gateway's `update.check` the update gate reads:
 * whether the server refuses this app until it updates, the versions on
 * both sides, empty on the server's side when the gateway could not reach
 * it, why it could not, and the server's release, empty from a gateway
 * before it.
 */
data class GateCheck(
    val required: Boolean,
    val clientVersion: String,
    val serverVersion: String,
    val serverError: String,
    val minClientVersion: String,
    val targetVersion: String,
)

data class Gate(
    val clientVersion: String,
    val serverVersion: String,
    val minClientVersion: String,
    val tag: String,
)

private val HANDSHAKE_ORIGIN = Regex("^(http://127\\.0\\.0\\.1:\\d{1,5})/\\?token=([A-Za-z0-9_-]+)$")

data class GatewayCall(val url: String, val token: String)

fun gatewayCall(handshakeUrl: String, verb: String): GatewayCall? {
    val (origin, token) = HANDSHAKE_ORIGIN.matchEntire(handshakeUrl)?.destructured ?: return null
    return GatewayCall("$origin/local/v1/$verb", token)
}

// The body is json.Encoder output of one object with the cli check nested
// under "cli"; org.json is a stub in the unit tests' android.jar. Every key
// read here is unique in that object, and a value with a backslash is not
// one this gate shows.
private fun stringField(json: String, name: String): String? =
    Regex("\"$name\"\\s*:\\s*\"([^\"\\\\]*)\"").find(json)?.groupValues?.get(1)

private fun boolField(json: String, name: String): Boolean? =
    Regex("\"$name\"\\s*:\\s*(true|false)").find(json)?.groupValues?.get(1)?.let { it == "true" }

/**
 * The check in an `update.check` response [body], or null when it is not
 * one. A gateway from before the update gate omits `client_update_required`,
 * which reads as not required.
 */
fun parseGateCheck(body: String): GateCheck? {
    val json = body.trim()
    if (!json.startsWith("{") || !json.endsWith("}")) return null
    val clientVersion = stringField(json, "version") ?: return null
    return GateCheck(
        required = boolField(json, "client_update_required") ?: false,
        clientVersion = clientVersion,
        serverVersion = stringField(json, "server_version") ?: "",
        serverError = stringField(json, "server_error") ?: "",
        minClientVersion = stringField(json, "min_client_version") ?: "",
        targetVersion = stringField(json, "target_version") ?: "",
    )
}

/**
 * The gate for [check], or null to let the dashboard through. The server
 * decides; this only refuses to gate on a version it cannot build a URL
 * from, so a dev build or a malformed answer never locks anyone out. What
 * to install is the server's release, else the oldest one it accepts.
 * GitHub's newest release is never offered: it can be newer than the
 * server, and an app newer than its server is refused in turn. An APK
 * older than the oldest accepted is refused as a downgrade or leaves the
 * gate up, so no target below it is offered either.
 */
fun gateFor(check: GateCheck?): Gate? {
    if (check == null || !check.required) return null
    val min = check.minClientVersion
    if (!isReleaseTag(check.clientVersion) || !isReleaseTag(min)) return null
    val target = check.targetVersion
    val tag = if (isReleaseTag(target) && compareReleaseTags(target, min) >= 0) target else min
    return Gate(check.clientVersion, check.serverVersion, min, tag)
}

/**
 * What an `update.check` response [body] means for the gate: a failure
 * when it is not a check, or when the gateway could not reach the server,
 * which then said nothing about this app. Only a check the server answered
 * may take a gate down.
 */
fun gateAnswer(body: String): Result<Gate?> {
    val check = parseGateCheck(body) ?: return Result.failure(IOException("update.check answered: ${body.trim()}"))
    if (check.serverVersion.isEmpty()) {
        return Result.failure(IOException("update.check did not reach the server: ${check.serverError}"))
    }
    return Result.success(gateFor(check))
}

// Semantic version precedence of two tags isReleaseTag accepts.
private fun compareReleaseTags(a: String, b: String): Int {
    val (aCore, aPre) = splitTag(a)
    val (bCore, bPre) = splitTag(b)
    for (i in 0 until 3) {
        val c = aCore[i].toBigInteger().compareTo(bCore[i].toBigInteger())
        if (c != 0) return c
    }
    // A release outranks its own prereleases.
    if (aPre == null || bPre == null) return (if (aPre == null) 1 else 0) - (if (bPre == null) 1 else 0)
    for ((x, y) in aPre.zip(bPre)) {
        val xn = x.toBigIntegerOrNull()
        val yn = y.toBigIntegerOrNull()
        val c =
            when {
                xn != null && yn != null -> xn.compareTo(yn)
                xn != null -> -1
                yn != null -> 1
                else -> x.compareTo(y)
            }
        if (c != 0) return c
    }
    return aPre.size.compareTo(bPre.size)
}

private fun splitTag(tag: String): Pair<List<String>, List<String>?> {
    val parts = tag.removePrefix("v").split('-', limit = 2)
    return parts[0].split('.') to parts.getOrNull(1)?.split('.')
}

fun gateDetails(gate: Gate, targets: List<UpdateTarget>): String =
    buildString {
        append("This Aether app (${gate.clientVersion}) is too old for the server ")
        append("(${gate.serverVersion} needs ${gate.minClientVersion} or newer).\n")
        append("Install ${gate.tag}:\n")
        for (target in targets) append(target.url).append('\n')
    }

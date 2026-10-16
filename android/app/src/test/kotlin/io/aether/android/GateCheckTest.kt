package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// What the Edge gateway's update.check answers, as json.Encoder writes it.
private fun body(
    version: String = "v0.7.0",
    latest: String? = "v0.8.1",
    serverVersion: String = "v0.8.0",
    required: Boolean? = true,
    minClient: String = "v0.8.0",
): String {
    val latestField = latest?.let { "\"latest\":\"$it\"," } ?: ""
    val gateFields = required?.let { ",\"client_update_required\":$it,\"min_client_version\":\"$minClient\"" } ?: ""
    return "{\"cli\":{\"version\":\"$version\",\"commit\":\"abc1234\",$latestField\"update_available\":true," +
        "\"dev\":false,\"disabled\":false,\"packaged\":\"Android app\",\"can_self_update\":false}," +
        "\"server_version\":\"$serverVersion\",\"server_behind\":false,\"supervised\":false$gateFields}\n"
}

private const val APK = "https://github.com/3xDevOps/Aether/releases/download/v0.8.1/aether-android.apk"

class GateCheckTest {
    @Test
    fun aRequiredUpdateGatesOnTheNewestRelease() {
        assertEquals(Gate("v0.7.0", "v0.8.0", "v0.8.0", "v0.8.1"), gateFor(parseGateCheck(body())))
    }

    @Test
    fun withoutTheNewestReleaseTheGateInstallsTheOneTheServerNeeds() {
        assertEquals("v0.8.0", gateFor(parseGateCheck(body(latest = null)))?.tag)
        assertEquals("v0.8.0", gateFor(parseGateCheck(body(latest = "garbage")))?.tag)
    }

    @Test
    fun anOptionalOrNoUpdateDoesNotGate() {
        // A newer release the server does not need yet is the banner's.
        assertNull(gateFor(parseGateCheck(body(required = false))))
        assertNull(gateFor(parseGateCheck(body(version = "v0.8.1", latest = "v0.8.1", required = false))))
        // A gateway from before the gate.
        assertNull(gateFor(parseGateCheck(body(required = null))))
    }

    @Test
    fun aFailedOrUnreadableCheckDoesNotGate() {
        assertNull(gateFor(null))
        for (text in listOf("", "{}", "not json", "{\"error\":{\"code\":-32003,\"message\":\"check for releases: offline\"}}")) {
            assertNull(text, gateFor(parseGateCheck(text)))
        }
    }

    @Test
    fun aVersionThatIsNotAReleaseNeverGates() {
        for (version in listOf("dev", "(devel)", "abc1234", "")) {
            assertNull(version, gateFor(parseGateCheck(body(version = version))))
            assertNull(version, gateFor(parseGateCheck(body(minClient = version))))
        }
        // A build past a tag still names that tag, and the server decided.
        assertEquals("v0.7.0-alpha.3-5-gabc123", gateFor(parseGateCheck(body(version = "v0.7.0-alpha.3-5-gabc123")))?.clientVersion)
    }

    @Test
    fun theGateClearsOnceTheUpdatedAppAsks() {
        assertEquals("v0.8.1", gateFor(parseGateCheck(body()))?.tag)
        assertNull(gateFor(parseGateCheck(body(version = "v0.8.1", required = false))))
    }

    @Test
    fun eachInstallSourceOpensItsOwnUpdate() {
        val gate = gateFor(parseGateCheck(body()))!!
        assertEquals(listOf(UpdateTarget(APK)), updateTargets(InstallSource.RELEASE, "io.aether.android", gate.tag))
        assertEquals(
            "market://details?id=io.aether.android",
            updateTargets(InstallSource.PLAY, "io.aether.android", gate.tag)?.first()?.url,
        )
    }

    @Test
    fun theDetailsNameBothVersionsAndTheDownload() {
        val gate = gateFor(parseGateCheck(body()))!!
        assertEquals(
            "This Aether app (v0.7.0) is too old for the server (v0.8.0 needs v0.8.0 or newer).\nInstall v0.8.1:\n$APK\n",
            gateDetails(gate, listOf(UpdateTarget(APK))),
        )
    }

    @Test
    fun onlyTheGatewaysHandshakeUrlBecomesACall() {
        assertEquals(
            GatewayCall("http://127.0.0.1:43210/local/v1/update.check", "tok_EN-1"),
            gatewayCall("http://127.0.0.1:43210/?token=tok_EN-1", "update.check"),
        )
        for (url in listOf("http://127.0.0.1:43210/", "https://example.com/?token=x", "http://127.0.0.1:43210/?token=a&b=c")) {
            assertNull(url, gatewayCall(url, "update.check"))
        }
    }
}

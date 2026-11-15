package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// What the Edge gateway's update.check answers, as json.Encoder writes it.
private fun body(
    version: String = "v0.8.7-beta.3",
    latest: String = "v0.8.7-beta.9",
    serverVersion: String = "v0.8.7-beta.5",
    target: String = "v0.8.7-beta.5",
    serverError: String? = null,
): String {
    val error = serverError?.let { ",\"server_error\":\"$it\"" } ?: ""
    return "{\"cli\":{\"version\":\"$version\",\"latest\":\"$latest\",\"update_available\":true,\"dev\":false}," +
        "\"server_version\":\"$serverVersion\",\"server_behind\":true$error," +
        "\"client_update_required\":false,\"min_client_version\":\"v0.8.7-beta.1\",\"target_version\":\"$target\"}\n"
}

class ServerReleaseCheckTest {
    @Test
    fun offersTheServersReleaseNotTheNewestOneOnGitHub() {
        val run = serverReleaseCheck(body())
        assertEquals(UpdateCheck("v0.8.7-beta.3", "v0.8.7-beta.5", true), parseUpdateCheck(run.line!!))
    }

    @Test
    fun aServerBuiltFromSourceMatchesItsReleaseTag() {
        val run = serverReleaseCheck(body(serverVersion = "v0.8.7-beta.5-3-gabcdef1-dirty"))
        assertEquals(UpdateCheck("v0.8.7-beta.3", "v0.8.7-beta.5", true), parseUpdateCheck(run.line!!))
    }

    @Test
    fun aServerWithBuildMetadataOrABareDirtyMarkMatchesItsReleaseTag() {
        for (server in listOf("v0.8.7-beta.5+b.412", "v0.8.7-beta.5-dirty")) {
            val run = serverReleaseCheck(body(serverVersion = server))
            assertEquals(server, UpdateCheck("v0.8.7-beta.3", "v0.8.7-beta.5", true), parseUpdateCheck(run.line!!))
        }
    }

    @Test
    fun aReleaseAheadOfTheServerWaitsForTheServerToUpdateFirst() {
        // The gateway's target is the channel's newest when the server is not ahead of this app.
        val run = serverReleaseCheck(body(version = "v0.8.7-beta.5", serverVersion = "v0.8.7-beta.5", target = "v0.8.7-beta.9"))
        assertEquals(UpdateCheck("v0.8.7-beta.5", "v0.8.7-beta.5", false), parseUpdateCheck(run.line!!))
    }

    @Test
    fun noTargetIsNoUpdate() {
        val run = serverReleaseCheck(body(target = ""))
        assertEquals(UpdateCheck("v0.8.7-beta.3", "v0.8.7-beta.3", false), parseUpdateCheck(run.line!!))
    }

    @Test
    fun aServerThatDidNotAnswerSaysNothingAboutThisApp() {
        val run = serverReleaseCheck(body(serverVersion = "", serverError = "dial tcp: connection refused"))
        assertNull(run.line)
        assertTrue(run.error, run.error.contains("did not reach the server: dial tcp: connection refused"))
    }

    @Test
    fun anythingElseIsNotACheck() {
        val run = serverReleaseCheck("<html>502</html>")
        assertNull(run.line)
        assertEquals("update.check answered: <html>502</html>", run.error)
    }
}

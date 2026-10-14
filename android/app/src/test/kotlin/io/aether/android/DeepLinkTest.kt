package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private const val BASE = "https://my-server.ts.net/"
private const val SERVER = "abcdefghijklmnopqrstuvwxyz"
private const val INVITATION = "inv-7k2m9q4xbd"
private const val SECRET = "abcdefghijk23456"
private const val JOIN =
    "aether://join?edge=https://edge.example.test&server=$SERVER&invitation=$INVITATION#$SECRET"
private const val CLAIM = "aether://claim?edge=https://edge.example.test&server=$SERVER#$SECRET"

class DeepLinkTest {
    @Test
    fun runLinkOpensThatRunOnTheDashboard() {
        assertEquals("$BASE?run=01k4x7m2qc", deepLinkUrl(BASE, "aether://run/01k4x7m2qc"))
        assertEquals("$BASE?run=run-7k2m9q4xbd", deepLinkUrl(BASE, "aether://run/run-7k2m9q4xbd"))
    }

    @Test
    fun schemeCaseIsAcceptedBecauseAndroidLowercasesItAnyway() {
        assertEquals("$BASE?run=01k4x7m2qc", deepLinkUrl(BASE, "AETHER://run/01k4x7m2qc"))
    }

    @Test
    fun otherSchemesAndHostsAreNotRunLinks() {
        assertNull(deepLinkUrl(BASE, "https://my-server.ts.net/"))
        assertNull(deepLinkUrl(BASE, "aether://workspace/01k4x7m2qc"))
        assertNull(deepLinkUrl(BASE, "not a url"))
    }

    @Test
    fun anIdThatIsNotARunIdIsRefused() {
        assertNull(deepLinkUrl(BASE, "aether://run/"))
        assertNull(deepLinkUrl(BASE, "aether://run/short"))
        assertNull(deepLinkUrl(BASE, "aether://run/01K4X7M2QC"))
        assertNull(deepLinkUrl(BASE, "aether://run/01k4x7m2qc/../../etc"))
        assertNull(deepLinkUrl(BASE, "aether://run/01k4x7m2qc&token=x"))
    }

    @Test
    fun aClaimLinkIsNotHandedToTheServersDashboard() {
        val claim = "aether://claim?edge=https://edge.example.test&server=abcdefghijklmnopqrstuvwxyz#abcdefghijk23456"
        assertNull(deepLinkUrl(BASE, claim))
    }

    @Test
    fun aJoinOrClaimLinkIsOnlyForEdgeMode() {
        assertTrue(isEdgeOnlyLink(JOIN))
        assertTrue(isEdgeOnlyLink(CLAIM))
        assertFalse(isEdgeOnlyLink("aether://run/01k4x7m2qc"))
        assertNull(deepLinkUrl(BASE, JOIN))
    }

    @Test
    fun edgeModeAppendsARunToTheTokenedUrlAndKeepsLinkSecretsInTheFragment() {
        assertEquals("&run=01k4x7m2qc", edgeDeepLinkSuffix("aether://run/01k4x7m2qc"))
        assertNull(edgeDeepLinkSuffix("aether://run/short"))
        assertEquals(
            "#join=aether%3A%2F%2Fjoin%3Fedge%3Dhttps%3A%2F%2Fedge.example.test%26server%3D$SERVER%26invitation%3D$INVITATION%23$SECRET",
            edgeDeepLinkSuffix(JOIN),
        )
        assertEquals(
            "#claim=aether%3A%2F%2Fclaim%3Fedge%3Dhttps%3A%2F%2Fedge.example.test%3A8443%26server%3D$SERVER%23$SECRET",
            edgeDeepLinkSuffix("aether://claim?edge=https://edge.example.test:8443&server=$SERVER#$SECRET"),
        )
        assertNull(edgeDeepLinkSuffix("https://edge.example.test/"))
        assertNull(edgeDeepLinkSuffix("aether://workspace/01k4x7m2qc"))
    }

    @Test
    fun anEdgeOriginIsHttpsOrLoopbackAndNothingElse() {
        fun join(edge: String) = edgeDeepLinkSuffix("aether://join?edge=$edge&server=$SERVER&invitation=$INVITATION#$SECRET")

        assertNotNull(join("https://edge.example.test:443"))
        assertNotNull(join("http://localhost:8080"))
        assertNotNull(join("http://127.0.0.1:8080"))
        assertNotNull(join("http://[::1]:8080"))
        assertNotNull(join("http://127.0.0.1:80"))
        assertNull(join("http://edge.example.test"))
        assertNull(join("ftp://edge.example.test"))
        assertNull(join("javascript:alert(1)"))
        assertNull(join("https://edge.example.test/"))
        assertNull(join("https://edge.example.test/path"))
        assertNull(join("https://edge.example.test?x=1"))
        assertNull(join("https://user@edge.example.test"))
        assertNull(join("https://EDGE.example.test"))
        assertNull(join("https://edge.example.test:0443"))
        assertNull(join("https://edge.example.test:65536"))
        assertNull(join("https://127.1"))
        assertNull(join("https://edge.example.test:"))
        assertNull(join(""))
    }

    @Test
    fun everyOtherPartOfAJoinOrClaimLinkIsChecked() {
        val query = "edge=https://edge.example.test&server=$SERVER"
        assertNotNull(edgeDeepLinkSuffix("aether://claim?$query#$SECRET"))
        assertNotNull(edgeDeepLinkSuffix("aether://claim/?$query#$SECRET"))
        // The names and counts are exact.
        assertNull(edgeDeepLinkSuffix("aether://claim?$query&invitation=$INVITATION#$SECRET"))
        assertNull(edgeDeepLinkSuffix("aether://claim?$query&extra=1#$SECRET"))
        assertNull(edgeDeepLinkSuffix("aether://claim?$query&server=$SERVER#$SECRET"))
        assertNull(edgeDeepLinkSuffix("aether://join?$query#$SECRET"))
        assertNull(edgeDeepLinkSuffix("aether://claim#$SECRET"))
        // Path, server id, invitation and fragment.
        assertNull(edgeDeepLinkSuffix("aether://claim/x?$query#$SECRET"))
        assertNull(edgeDeepLinkSuffix("aether://claim?edge=https://edge.example.test&server=${SERVER.uppercase()}#$SECRET"))
        assertNull(edgeDeepLinkSuffix("aether://claim?edge=https://edge.example.test&server=${SERVER.dropLast(1)}#$SECRET"))
        assertNull(edgeDeepLinkSuffix(JOIN.replace(INVITATION, "inv-iiiiiiiiii")))
        assertNull(edgeDeepLinkSuffix(JOIN.replace(INVITATION, "inv-uuuuuuuuuu")))
        assertNull(edgeDeepLinkSuffix(JOIN.removeSuffix(SECRET).removeSuffix("#")))
        assertNull(edgeDeepLinkSuffix(JOIN.removeSuffix("6")))
        assertNull(edgeDeepLinkSuffix("${JOIN}2"))
        assertNull(edgeDeepLinkSuffix(JOIN.replace("#$SECRET", "#${SECRET.uppercase()}")))
        assertNull(edgeDeepLinkSuffix(JOIN.replace("aether://join", "aether://JOIN")))
        assertNull(edgeDeepLinkSuffix(JOIN.replace("invitation=$INVITATION", "invitation=%zz")))
    }
}

package io.aether.android

import java.net.URLEncoder
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

private const val BASE = "https://my-server.ts.net/"
private const val SERVER = "abcdefghijklmnopqrstuvwxyz"
private const val SECRET = "abcdefghijk23456"

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
    fun joinAndClaimLinksOpenTheConnectStepWithTheWholeLink() {
        val join = "aether://join?edge=https://edge.example.test&server=$SERVER&invitation=inv-7k2m9q4xbd#$SECRET"
        assertEquals("$BASE#join=${URLEncoder.encode(join, "UTF-8")}", deepLinkUrl(BASE, join))
        val claim = "aether://claim?edge=https%3A%2F%2Fedge.example.test%3A8443&server=$SERVER#$SECRET"
        assertEquals("$BASE#claim=${URLEncoder.encode(claim, "UTF-8")}", deepLinkUrl(BASE, claim))
    }

    @Test
    fun aClaimLinkToABracketedIpv6EdgeOpensTheConnectStep() {
        val claim = "aether://claim?edge=https%3A%2F%2F%5B2001%3Adb8%3A%3A1%5D%3A8443&server=$SERVER#$SECRET"
        assertEquals("$BASE#claim=${URLEncoder.encode(claim, "UTF-8")}", deepLinkUrl(BASE, claim))
    }

    @Test
    fun aJoinOrClaimLinkWithAnyPartOutOfShapeIsRefused() {
        val edge = "edge=https://edge.example.test"
        assertNull(deepLinkUrl(BASE, "aether://claim?$edge&server=$SERVER"))
        assertNull(deepLinkUrl(BASE, "aether://claim?$edge&server=$SERVER#short"))
        assertNull(deepLinkUrl(BASE, "aether://claim?edge=http://edge.example.test&server=$SERVER#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://claim?edge=https://edge.example.test/x&server=$SERVER#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://claim?$edge&server=${SERVER.uppercase()}#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://claim?$edge&server=$SERVER&token=x#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://claim?$edge&server=$SERVER&server=$SERVER#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://join?$edge&server=$SERVER#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://join?$edge&server=$SERVER&invitation=inv-7K2M9Q4XBD#$SECRET"))
        assertNull(deepLinkUrl(BASE, "aether://join/x?$edge&server=$SERVER&invitation=inv-7k2m9q4xbd#$SECRET"))
    }
}

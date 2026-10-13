package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class GatewayTest {
    private val line = """{"url":"http://127.0.0.1:41233/?token=aB3_-xY9","addr":"127.0.0.1:41233"}"""

    @Test
    fun theGatewaysLineIsTheHandshake() {
        assertEquals(
            Handshake("http://127.0.0.1:41233/?token=aB3_-xY9", "127.0.0.1:41233"),
            parseHandshake(line),
        )
        assertEquals(parseHandshake(line), parseHandshake("  $line\n"))
    }

    @Test
    fun anyOtherLineIsSkipped() {
        assertNull(parseHandshake(""))
        assertNull(parseHandshake("warning: ssh agent not running"))
        assertNull(parseHandshake("""{"url":"http://127.0.0.1:41233/?token=x"}"""))
        assertNull(parseHandshake("""{"url":"http://127.0.0.1:41233/?token=x","addr":"127.0.0.1:41233"} trailing"""))
    }

    @Test
    fun aUrlThatIsNotTheTokenedLoopbackOneIsRefused() {
        fun handshake(url: String, addr: String = "127.0.0.1:41233") =
            parseHandshake("""{"url":"$url","addr":"$addr"}""")

        assertNull(handshake("http://evil.example:41233/?token=x"))
        assertNull(handshake("http://127.0.0.1.evil.example:41233/?token=x"))
        assertNull(handshake("http://localhost:41233/?token=x"))
        assertNull(handshake("https://127.0.0.1:41233/?token=x"))
        assertNull(handshake("http://127.0.0.1:41233/?token=x&next=http://evil.example"))
        assertNull(handshake("http://127.0.0.1:41233/?token="))
        assertNull(handshake("http://127.0.0.1:41233/other?token=x"))
        assertNull(handshake("http://user@127.0.0.1:41233/?token=x"))
        assertNull(handshake("http://127.0.0.1:41233/?token=x#frag"))
        assertNull(handshake("http://127.0.0.1:99999/?token=x", "127.0.0.1:99999"))
    }

    @Test
    fun theAddrMustAgreeWithTheUrl() {
        val url = "http://127.0.0.1:41233/?token=x"
        assertNull(parseHandshake("""{"url":"$url","addr":"127.0.0.1:41234"}"""))
        assertNull(parseHandshake("""{"url":"$url","addr":"evil.example:41233"}"""))
        assertNull(parseHandshake("""{"url":"$url","addr":"0.0.0.0:41233"}"""))
    }

    @Test
    fun aJsonEscapeIsNotTheShapeTheGatewayPrints() {
        val backslash = "\\"
        fun handshake(url: String, addr: String = "127.0.0.1:41233") =
            parseHandshake("{\"url\":\"$url\",\"addr\":\"$addr\"}")

        assertNull(handshake("http://127.0.0.1:41233/?token=x${backslash}u0026y"))
        assertNull(handshake("http://127.0.0.1:4123${backslash}u0033/?token=x"))
        assertNull(handshake("http://127.0.0.1:41233/?token=x", "127.0.0.1:41233${backslash}n"))
    }

    @Test
    fun resolvConfListsEachServerAndKeepsAnIpv6Zone() {
        assertEquals("", resolvConf(emptyList()))
        assertEquals(
            "nameserver 192.168.1.1\nnameserver fe80::1%wlan0\n",
            resolvConf(listOf("192.168.1.1", "fe80::1%wlan0")),
        )
    }

    @Test
    fun theCaDirectoryIsTheApexStoreWhereItExists() {
        assertEquals("/apex/com.android.conscrypt/cacerts", caDirectory(apexExists = true))
        assertEquals("/system/etc/security/cacerts", caDirectory(apexExists = false))
    }

    @Test
    fun aStoredAddressWithoutAModeIsTailscale() {
        assertEquals(ConnectionMode.TAILSCALE, resolveMode(null, "https://my-server.ts.net/"))
        assertEquals(ConnectionMode.TAILSCALE, resolveMode("tailscale", null))
        assertEquals(ConnectionMode.EDGE, resolveMode("edge", "https://my-server.ts.net/"))
        assertEquals(ConnectionMode.EDGE, resolveMode("edge", null))
        assertNull(resolveMode(null, null))
        assertEquals(ConnectionMode.TAILSCALE, resolveMode("unknown", "https://my-server.ts.net/"))
        assertNull(resolveMode("unknown", null))
    }
}

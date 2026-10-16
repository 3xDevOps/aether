package io.aether.android

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Android's java.util.regex is ICU, which refuses a "{" or "}" that is not
 * part of a quantifier. The JVM these tests run on accepts both, so such a
 * pattern passes every other test here and then throws from its class
 * initializer on the phone.
 */
class RegexDialectTest {
    private val literal = Regex("""Regex\("((?:[^"\\]|\\.)*)"\)""")
    private val quantifier = Regex("""\{\d+(,\d*)?}""")

    @Test
    fun everyPatternKeepsItsBracesToQuantifiers() {
        val sources = File("src/main/kotlin").walk().filter { it.extension == "kt" }.toList()
        assertTrue(sources.isNotEmpty())
        val refused =
            sources.flatMap { file ->
                literal.findAll(file.readText()).mapNotNull { match ->
                    val pattern = unescapeKotlin(match.groupValues[1])
                    strayBrace(pattern)?.let { "${file.name}: index $it of $pattern" }
                }.toList()
            }
        assertEquals(emptyList<String>(), refused)
    }

    @Test
    fun aStrayBraceIsFound() {
        assertEquals(6, strayBrace("""^\{"a"}$"""))
        assertEquals(1, strayBrace("""a{b"""))
        assertEquals(null, strayBrace("""^\{"a"\}[{}]\d{1,5}x{2,}$"""))
    }

    private fun unescapeKotlin(text: String): String {
        val out = StringBuilder()
        var i = 0
        while (i < text.length) {
            if (text[i] == '\\' && i + 1 < text.length) i++
            out.append(text[i])
            i++
        }
        return out.toString()
    }

    /** The index of the first brace outside a class, an escape and a quantifier. */
    private fun strayBrace(pattern: String): Int? {
        var inClass = false
        var i = 0
        while (i < pattern.length) {
            val c = pattern[i]
            when {
                c == '\\' -> i++
                inClass -> if (c == ']') inClass = false
                c == '[' -> inClass = true
                c == '{' -> {
                    val match = quantifier.matchAt(pattern, i) ?: return i
                    i = match.range.last
                }
                c == '}' -> return i
            }
            i++
        }
        return null
    }
}

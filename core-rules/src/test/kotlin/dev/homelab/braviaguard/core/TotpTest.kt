package dev.homelab.braviaguard.core

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class Base32Test {

    // RFC 4648 section 10, minus the '=' padding we do not emit.
    private val vectors = listOf(
        "f" to "MY",
        "fo" to "MZXQ",
        "foo" to "MZXW6",
        "foob" to "MZXW6YQ",
        "fooba" to "MZXW6YTB",
        "foobar" to "MZXW6YTBOI",
    )

    @Test
    fun `encodes the RFC 4648 vectors`() {
        vectors.forEach { (plain, encoded) ->
            assertEquals(encoded, Base32.encode(plain.toByteArray()), "encode($plain)")
        }
    }

    @Test
    fun `decodes the RFC 4648 vectors`() {
        vectors.forEach { (plain, encoded) ->
            assertEquals(plain, Base32.decode(encoded).decodeToString(), "decode($encoded)")
        }
    }

    @Test
    fun `tolerates padding, lower case, spaces and the dashes people type`() {
        assertEquals("foobar", Base32.decode("MZXW6YTBOI======").decodeToString())
        assertEquals("foobar", Base32.decode("mzxw6ytboi").decodeToString())
        assertEquals("foobar", Base32.decode("MZXW 6YTB OI").decodeToString())
        assertEquals("foobar", Base32.decode("MZXW-6YTB-OI").decodeToString())
    }

    @Test
    fun `round trips arbitrary bytes`() {
        val bytes = ByteArray(37) { (it * 7 + 3).toByte() }
        assertTrue(bytes.contentEquals(Base32.decode(Base32.encode(bytes))))
    }

    @Test
    fun `empty input stays empty`() {
        assertEquals("", Base32.encode(ByteArray(0)))
        assertEquals(0, Base32.decode("").size)
    }
}

class TotpTest {

    private val sha1Secret = "12345678901234567890".toByteArray()
    private val sha256Secret = "12345678901234567890123456789012".toByteArray()

    @Test
    fun `matches the RFC 6238 SHA1 test vectors`() {
        val vectors = listOf(
            59L to "94287082",
            1111111109L to "07081804",
            1111111111L to "14050471",
            1234567890L to "89005924",
            2000000000L to "69279037",
            20000000000L to "65353130",
        )
        vectors.forEach { (seconds, expected) ->
            assertEquals(
                expected,
                Totp.code(sha1Secret, seconds * 1000, digits = 8, algorithm = "HmacSHA1"),
                "T=$seconds",
            )
        }
    }

    @Test
    fun `matches the RFC 6238 SHA256 test vectors`() {
        val vectors = listOf(
            59L to "46119246",
            1111111109L to "68084774",
            1111111111L to "67062674",
            1234567890L to "91819424",
            2000000000L to "90698825",
            20000000000L to "77737706",
        )
        vectors.forEach { (seconds, expected) ->
            assertEquals(
                expected,
                Totp.code(sha256Secret, seconds * 1000, digits = 8, algorithm = "HmacSHA256"),
                "T=$seconds",
            )
        }
    }

    @Test
    fun `counter advances once per step`() {
        assertEquals(1L, Totp.counterAt(59_000))
        assertEquals(2L, Totp.counterAt(60_000))
        assertEquals(2L, Totp.counterAt(89_999))
        // RFC 6238's own worked example: T=1111111109 is counter 0x23523EC, and the very
        // next second rolls into the following step.
        assertEquals(0x23523ECL, Totp.counterAt(1111111109L * 1000))
        assertEquals(37037036L, Totp.counterAt(1111111109L * 1000))
        assertEquals(37037037L, Totp.counterAt(1111111111L * 1000))
    }

    @Test
    fun `the code is stable for the whole step and changes at the boundary`() {
        val base = 1_700_000_040_000L // exactly on a 30s boundary
        val code = Totp.code(sha1Secret, base)
        assertEquals(code, Totp.code(sha1Secret, base + 29_999))
        assertFalse(code == Totp.code(sha1Secret, base + 30_000))
    }

    @Test
    fun `default code is six digits`() {
        val code = Totp.code(sha1Secret, 1_700_000_000_000L)
        assertEquals(6, code.length)
        assertTrue(code.all { it.isDigit() })
    }

    @Test
    fun `constant time compare still compares correctly`() {
        assertTrue(Totp.constantTimeEquals("123456", "123456"))
        assertFalse(Totp.constantTimeEquals("123456", "123457"))
        assertFalse(Totp.constantTimeEquals("123456", "12345"))
    }
}

class OfflineCodeGateTest {

    private val secret = "12345678901234567890".toByteArray()
    private val now = 1_700_000_000_000L
    private val counter = Totp.counterAt(now)

    private fun codeFor(delta: Long) = Totp.hotp(secret, counter + delta)

    private fun gate(lastAccepted: Long = OfflineCodeGate.NEVER_REDEEMED) =
        OfflineCodeGate(secret, lastAcceptedCounter = lastAccepted)

    @Test
    fun `accepts the code showing on the phone right now`() {
        assertEquals(OfflineCodeResult.ACCEPTED, gate().redeem(codeFor(0), now))
    }

    @Test
    fun `each code works once`() {
        val gate = gate()
        assertEquals(OfflineCodeResult.ACCEPTED, gate.redeem(codeFor(0), now))
        assertEquals(OfflineCodeResult.ALREADY_USED, gate.redeem(codeFor(0), now))
    }

    @Test
    fun `the next code still works after one has been spent`() {
        val gate = gate()
        assertEquals(OfflineCodeResult.ACCEPTED, gate.redeem(codeFor(0), now))
        assertEquals(OfflineCodeResult.ACCEPTED, gate.redeem(codeFor(1), now + 30_000))
        assertEquals(counter + 1, gate.lastAcceptedCounter)
    }

    @Test
    fun `an older code is refused once a newer one has been used`() {
        val gate = gate()
        gate.redeem(codeFor(0), now)
        assertEquals(OfflineCodeResult.ALREADY_USED, gate.redeem(codeFor(-1), now))
    }

    @Test
    fun `tolerates a TV clock that is up to a minute out`() {
        assertEquals(OfflineCodeResult.ACCEPTED, gate().redeem(codeFor(-2), now))
        assertEquals(OfflineCodeResult.ACCEPTED, gate().redeem(codeFor(2), now))
    }

    @Test
    fun `refuses a clock skew beyond the window`() {
        assertEquals(OfflineCodeResult.WRONG_CODE, gate().redeem(codeFor(-3), now))
        assertEquals(OfflineCodeResult.WRONG_CODE, gate().redeem(codeFor(3), now))
    }

    @Test
    fun `refuses a code from a different secret`() {
        val other = Totp.hotp("09876543210987654321".toByteArray(), counter)
        assertEquals(OfflineCodeResult.WRONG_CODE, gate().redeem(other, now))
    }

    @Test
    fun `refuses input of the wrong length instead of guessing`() {
        assertEquals(OfflineCodeResult.WRONG_CODE, gate().redeem("12345", now))
        assertEquals(OfflineCodeResult.WRONG_CODE, gate().redeem("", now))
    }

    @Test
    fun `ignores separators a parent might read out loud`() {
        assertEquals(OfflineCodeResult.ACCEPTED, gate().redeem(spaced(codeFor(0)), now))
    }

    @Test
    fun `a restored counter survives a reboot`() {
        // Simulates the gate being rebuilt from persisted state after the TV restarted.
        val restored = gate(lastAccepted = counter)
        assertEquals(OfflineCodeResult.ALREADY_USED, restored.redeem(codeFor(0), now))
        assertEquals(OfflineCodeResult.ACCEPTED, restored.redeem(codeFor(1), now + 30_000))
    }

    private fun spaced(code: String) = "${code.take(3)} ${code.drop(3)}"
}

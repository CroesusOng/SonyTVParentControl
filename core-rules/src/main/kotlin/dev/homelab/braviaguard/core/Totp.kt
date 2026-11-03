package dev.homelab.braviaguard.core

import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/** RFC 4648 base32, because that is what every authenticator app speaks. */
object Base32 {
    private const val ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

    fun encode(bytes: ByteArray): String {
        if (bytes.isEmpty()) return ""
        val out = StringBuilder((bytes.size * 8 + 4) / 5)
        var buffer = 0
        var bits = 0
        for (b in bytes) {
            buffer = (buffer shl 8) or (b.toInt() and 0xff)
            bits += 8
            while (bits >= 5) {
                out.append(ALPHABET[(buffer shr (bits - 5)) and 0x1f])
                bits -= 5
            }
        }
        if (bits > 0) {
            out.append(ALPHABET[(buffer shl (5 - bits)) and 0x1f])
        }
        return out.toString()
    }

    fun decode(text: String): ByteArray {
        val clean = text.uppercase().filter { it != '=' && !it.isWhitespace() && it != '-' }
        val out = ArrayList<Byte>(clean.length * 5 / 8 + 1)
        var buffer = 0
        var bits = 0
        for (c in clean) {
            val value = ALPHABET.indexOf(c)
            require(value >= 0) { "not base32: '$c'" }
            buffer = (buffer shl 5) or value
            bits += 5
            if (bits >= 8) {
                out.add(((buffer shr (bits - 8)) and 0xff).toByte())
                bits -= 8
            }
        }
        return out.toByteArray()
    }
}

/**
 * The offline escape code.
 *
 * When the Raspberry Pi is unreachable there is no way for the parent app to push a grant,
 * so the TV has to be able to verify something on its own. A shared TOTP secret is the
 * smallest thing that works: the phone computes the code from the secret it cached (behind
 * Face ID), the TV verifies it against the same secret, and neither needs the network.
 *
 * Defaults are HMAC-SHA1 / 6 digits / 30s on purpose rather than something stronger: it
 * makes the secret importable into any ordinary authenticator app, which is the backup plan
 * if the parent's phone is lost.
 */
object Totp {
    const val DEFAULT_STEP_SECONDS = 30
    const val DEFAULT_DIGITS = 6
    const val DEFAULT_ALGORITHM = "HmacSHA1"

    private val POW10 = intArrayOf(1, 10, 100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000, 100_000_000)

    fun counterAt(timeMillis: Long, stepSeconds: Int = DEFAULT_STEP_SECONDS): Long =
        Math.floorDiv(Math.floorDiv(timeMillis, 1000L), stepSeconds.toLong())

    fun code(
        secret: ByteArray,
        timeMillis: Long,
        stepSeconds: Int = DEFAULT_STEP_SECONDS,
        digits: Int = DEFAULT_DIGITS,
        algorithm: String = DEFAULT_ALGORITHM,
    ): String = hotp(secret, counterAt(timeMillis, stepSeconds), digits, algorithm)

    fun hotp(
        secret: ByteArray,
        counter: Long,
        digits: Int = DEFAULT_DIGITS,
        algorithm: String = DEFAULT_ALGORITHM,
    ): String {
        require(digits in 1..8) { "digits must be 1..8, was $digits" }
        val message = ByteArray(8)
        var value = counter
        for (i in 7 downTo 0) {
            message[i] = (value and 0xff).toByte()
            value = value ushr 8
        }
        val mac = Mac.getInstance(algorithm)
        mac.init(SecretKeySpec(secret, algorithm))
        val hash = mac.doFinal(message)

        val offset = hash[hash.size - 1].toInt() and 0x0f
        val binary = ((hash[offset].toInt() and 0x7f) shl 24) or
            ((hash[offset + 1].toInt() and 0xff) shl 16) or
            ((hash[offset + 2].toInt() and 0xff) shl 8) or
            (hash[offset + 3].toInt() and 0xff)

        return (binary % POW10[digits]).toString().padStart(digits, '0')
    }

    /** Compares without leaking length or position through timing. */
    fun constantTimeEquals(a: String, b: String): Boolean {
        if (a.length != b.length) return false
        var diff = 0
        for (i in a.indices) {
            diff = diff or (a[i].code xor b[i].code)
        }
        return diff == 0
    }
}

enum class OfflineCodeResult {
    ACCEPTED,

    /** No counter within the skew window produces this code. */
    WRONG_CODE,

    /** Correct, but this counter was already redeemed — replay. */
    ALREADY_USED,
}

/**
 * Verifies offline escape codes, once each.
 *
 * [skewSteps] is 2 (±60s) rather than the usual 1 because a BRAVIA that has just been
 * unplugged comes back with a wrong clock until it reaches an NTP server, and a code the
 * parent can see on their phone but the TV rejects is the worst possible failure here.
 */
class OfflineCodeGate(
    private val secret: ByteArray,
    private val stepSeconds: Int = Totp.DEFAULT_STEP_SECONDS,
    private val digits: Int = Totp.DEFAULT_DIGITS,
    private val algorithm: String = Totp.DEFAULT_ALGORITHM,
    private val skewSteps: Int = 2,
    lastAcceptedCounter: Long = NEVER_REDEEMED,
) {
    var lastAcceptedCounter: Long = lastAcceptedCounter
        private set

    fun redeem(code: String, nowMillis: Long): OfflineCodeResult {
        val entered = code.filter { it.isDigit() }
        if (entered.length != digits) return OfflineCodeResult.WRONG_CODE

        val centre = Totp.counterAt(nowMillis, stepSeconds)
        for (delta in -skewSteps..skewSteps) {
            val counter = centre + delta
            if (!Totp.constantTimeEquals(entered, Totp.hotp(secret, counter, digits, algorithm))) continue
            return if (counter <= lastAcceptedCounter) {
                OfflineCodeResult.ALREADY_USED
            } else {
                lastAcceptedCounter = counter
                OfflineCodeResult.ACCEPTED
            }
        }
        return OfflineCodeResult.WRONG_CODE
    }

    companion object {
        const val NEVER_REDEEMED = -1L
    }
}

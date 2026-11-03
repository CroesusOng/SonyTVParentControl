package dev.homelab.braviaguard.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.util.Log
import java.security.GeneralSecurityException
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The two things in this app that must not sit in plaintext: the device bearer token and
 * the offline escape-code secret. Anyone who reads the escape secret off the TV can mint
 * unlock codes forever, so it is kept under an AES-256-GCM key that lives in the
 * AndroidKeyStore and cannot be exported from the device.
 *
 * Hand-rolled on top of SharedPreferences rather than using androidx.security's
 * EncryptedSharedPreferences, whose stable release is 1.0.0 with only alphas after it —
 * this is ~60 lines of well-trodden Keystore usage with no dependency to age badly.
 */
class SecretStore(context: Context) {

    private val prefs = context.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    fun put(key: String, value: String?) {
        if (value == null) {
            remove(key)
            return
        }
        try {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.ENCRYPT_MODE, secretKey())
            val ciphertext = cipher.doFinal(value.toByteArray())
            prefs.edit()
                .putString(key, "${b64(cipher.iv)}:${b64(ciphertext)}")
                .apply()
        } catch (e: GeneralSecurityException) {
            Log.e(TAG, "could not store '$key'", e)
        }
    }

    fun get(key: String): String? {
        val stored = prefs.getString(key, null) ?: return null
        val parts = stored.split(':', limit = 2)
        if (parts.size != 2) return null
        return try {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(TAG_BITS, unb64(parts[0])))
            cipher.doFinal(unb64(parts[1])).decodeToString()
        } catch (e: GeneralSecurityException) {
            // Happens if the Keystore key was invalidated (factory reset, OS upgrade).
            // The value is unrecoverable, so drop it and make the caller re-pair.
            Log.w(TAG, "'$key' is no longer decryptable; dropping it", e)
            remove(key)
            null
        }
    }

    fun remove(key: String) = prefs.edit().remove(key).apply()

    fun clear() = prefs.edit().clear().apply()

    private fun secretKey(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (keyStore.getEntry(ALIAS, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }

        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE).apply {
            init(
                KeyGenParameterSpec.Builder(
                    ALIAS,
                    KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
                )
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .setRandomizedEncryptionRequired(true)
                    .build(),
            )
        }.generateKey()
    }

    private fun b64(bytes: ByteArray) = Base64.encodeToString(bytes, Base64.NO_WRAP)
    private fun unb64(text: String) = Base64.decode(text, Base64.NO_WRAP)

    companion object {
        private const val TAG = "SecretStore"
        private const val FILE = "guard_secrets"
        private const val ANDROID_KEYSTORE = "AndroidKeyStore"
        private const val ALIAS = "braviaguard.secrets.v1"
        private const val TRANSFORMATION = "AES/GCM/NoPadding"
        private const val TAG_BITS = 128

        const val KEY_DEVICE_TOKEN = "device_token"
        const val KEY_ESCAPE_SECRET = "escape_secret_base32"
    }
}

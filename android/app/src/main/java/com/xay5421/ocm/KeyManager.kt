package com.xay5421.ocm

import android.content.Context
import android.util.Base64
import net.i2p.crypto.eddsa.EdDSAPrivateKey
import net.i2p.crypto.eddsa.EdDSAPublicKey
import net.i2p.crypto.eddsa.spec.EdDSANamedCurveTable
import net.i2p.crypto.eddsa.spec.EdDSAPrivateKeySpec
import net.i2p.crypto.eddsa.spec.EdDSAPublicKeySpec
import java.io.File
import java.nio.ByteBuffer
import java.security.KeyPair
import java.security.SecureRandom

/**
 * App-wide ed25519 identity. The 32-byte seed lives in app-private storage;
 * the key pair is deterministically derived from it.
 */
object KeyManager {

    private fun seedFile(context: Context) = File(context.filesDir, "id_ed25519.seed")

    fun ensureKey(context: Context) {
        val f = seedFile(context)
        if (!f.exists()) {
            val seed = ByteArray(32)
            SecureRandom().nextBytes(seed)
            f.writeBytes(seed)
        }
    }

    fun keyPair(context: Context): KeyPair {
        ensureKey(context)
        val seed = seedFile(context).readBytes()
        val spec = EdDSANamedCurveTable.getByName(EdDSANamedCurveTable.ED_25519)
        val priv = EdDSAPrivateKey(EdDSAPrivateKeySpec(seed, spec))
        val pub = EdDSAPublicKey(EdDSAPublicKeySpec(priv.a, spec))
        return KeyPair(pub, priv)
    }

    /** Base64 of the 32-byte seed, for backup inside the exported config. */
    fun exportSeedBase64(context: Context): String {
        ensureKey(context)
        return Base64.encodeToString(seedFile(context).readBytes(), Base64.NO_WRAP)
    }

    /** Restore the identity from a previously exported seed. */
    fun importSeedBase64(context: Context, b64: String) {
        val seed = Base64.decode(b64.trim(), Base64.DEFAULT)
        require(seed.size == 32) { "key_seed 必须是 32 字节的 base64" }
        seedFile(context).writeBytes(seed)
    }

    fun publicKeyOpenSsh(context: Context): String {
        val pub = keyPair(context).public as EdDSAPublicKey
        val type = "ssh-ed25519".toByteArray(Charsets.US_ASCII)
        val key = pub.abyte
        val blob = ByteBuffer.allocate(4 + type.size + 4 + key.size).apply {
            putInt(type.size); put(type)
            putInt(key.size); put(key)
        }.array()
        return "ssh-ed25519 " + Base64.encodeToString(blob, Base64.NO_WRAP) + " ocm-android"
    }
}

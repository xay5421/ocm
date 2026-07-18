package com.xay5421.ocm

import android.app.Application
import org.bouncycastle.jce.provider.BouncyCastleProvider
import java.security.Security

class App : Application() {

    override fun onCreate() {
        super.onCreate()
        installFullBouncyCastle()
    }

    companion object {
        /**
         * Android ships a stripped-down BouncyCastle registered as "BC" that
         * lacks X25519 and other algorithms sshj needs. Replace it with the
         * full provider bundled via sshj's bcprov dependency.
         */
        fun installFullBouncyCastle() {
            val current = Security.getProvider(BouncyCastleProvider.PROVIDER_NAME)
            if (current != null && current !is BouncyCastleProvider) {
                Security.removeProvider(BouncyCastleProvider.PROVIDER_NAME)
            }
            if (Security.getProvider(BouncyCastleProvider.PROVIDER_NAME) == null) {
                Security.insertProviderAt(BouncyCastleProvider(), 1)
            }
        }
    }
}

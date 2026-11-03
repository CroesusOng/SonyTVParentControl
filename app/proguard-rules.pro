# kotlinx.serialization keeps generated serializers via @Serializable companions.
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.**
-keepclassmembers class dev.homelab.braviaguard.**$$serializer { *; }
-keepclassmembers class dev.homelab.braviaguard.core.**$$serializer { *; }
-keepclasseswithmembers class dev.homelab.braviaguard.** {
    public static ** Companion;
}

# AccessibilityService is instantiated by the system from the manifest entry.
-keep class dev.homelab.braviaguard.enforce.GuardAccessibilityService { *; }
-keep class dev.homelab.braviaguard.sync.** extends androidx.work.ListenableWorker { *; }

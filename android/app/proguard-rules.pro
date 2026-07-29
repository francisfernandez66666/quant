-keep class com.liangzai.quant.MainActivity { *; }
-keep class com.liangzai.quant.QuantService { *; }
-keep class com.liangzai.quant.JNIBridge { *; }
-keep class com.liangzai.quant.EngineManager { *; }
-keep class com.liangzai.quant.SecurityCheck { *; }

-keep class com.liangzai.quant.JNIBridge { native <methods>; }

-optimizations !code/simplification/arithmetic,!code/simplification/cast,!field/*,!class/merging/*
-repackageclasses 'q'
-allowaccessmodification
-dontusemixedcaseclassnames
-flattenpackagehierarchy 'q'

-assumenosideeffects class android.util.Log {
    public static boolean isLoggable(java.lang.String, int);
    public static int v(...);
    public static int d(...);
    public static int i(...);
    public static int w(...);
}
-assumenosideeffects class android.widget.Toast {
    public static android.widget.Toast makeText(android.content.Context, java.lang.CharSequence, int);
    public void show();
}

-keepattributes Signature
-keepattributes *Annotation*
-keep class com.google.gson.** { *; }
-keep class android.webkit.** { *; }
-keepattributes SourceFile,LineNumberTable
-renameSourceFileAttribute 'S'
-keep class java.lang.Throwable { *; }
-keep class java.lang.Exception { *; }
-assumenosideeffects class java.io.PrintStream {
    public void println(...);
    public void print(...);
}

-keepattributes InnerClasses
-keep class * implements android.os.Parcelable { *; }
-dontwarn com.liangzai.quant.**

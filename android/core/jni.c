//go:build android

#include <jni.h>

extern int sol_start(char *server_url, char *token);
extern void sol_stop(void);
extern int sol_running(void);

static jint native_start(JNIEnv *env, jclass clazz, jstring server_url, jstring token) {
    (void)clazz;
    if (server_url == NULL || token == NULL) {
        return 2;
    }

    const char *server = (*env)->GetStringUTFChars(env, server_url, NULL);
    if (server == NULL) {
        return 3;
    }
    const char *secret = (*env)->GetStringUTFChars(env, token, NULL);
    if (secret == NULL) {
        (*env)->ReleaseStringUTFChars(env, server_url, server);
        return 3;
    }

    int rc = sol_start((char *)server, (char *)secret);
    (*env)->ReleaseStringUTFChars(env, token, secret);
    (*env)->ReleaseStringUTFChars(env, server_url, server);
    return rc;
}

static void native_stop(JNIEnv *env, jclass clazz) {
    (void)env;
    (void)clazz;
    sol_stop();
}

static jboolean native_is_running(JNIEnv *env, jclass clazz) {
    (void)env;
    (void)clazz;
    return sol_running() ? JNI_TRUE : JNI_FALSE;
}

static JNINativeMethod methods[] = {
    {"nativeStart", "(Ljava/lang/String;Ljava/lang/String;)I", (void *)native_start},
    {"nativeStop", "()V", (void *)native_stop},
    {"nativeIsRunning", "()Z", (void *)native_is_running},
};

JNIEXPORT jint JNICALL JNI_OnLoad(JavaVM *vm, void *reserved) {
    (void)reserved;
    JNIEnv *env = NULL;
    if ((*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6) != JNI_OK) {
        return JNI_ERR;
    }

    jclass clazz = (*env)->FindClass(env, "bond/huggy/sol/SolCore");
    if (clazz == NULL) {
        return JNI_ERR;
    }
    if ((*env)->RegisterNatives(env, clazz, methods, sizeof(methods) / sizeof(methods[0])) < 0) {
        (*env)->DeleteLocalRef(env, clazz);
        return JNI_ERR;
    }
    (*env)->DeleteLocalRef(env, clazz);
    return JNI_VERSION_1_6;
}

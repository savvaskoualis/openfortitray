#include <dispatch/dispatch.h>
#include <pthread.h>
#include <stdint.h>
#include "mainthread_darwin.h"
#include "_cgo_export.h"

static void oft_invoke_trampoline(void *ctx) {
    goMainThreadTrampoline((unsigned long)(uintptr_t)ctx);
}

void oft_run_on_main_thread(unsigned long handle) {
    void *ctx = (void *)(uintptr_t)handle;
    if (pthread_main_np()) {
        oft_invoke_trampoline(ctx);
    } else {
        dispatch_sync_f(dispatch_get_main_queue(), ctx, oft_invoke_trampoline);
    }
}
